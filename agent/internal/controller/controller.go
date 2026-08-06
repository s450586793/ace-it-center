// Package controller 负责协调 Agent 生命周期且不暴露密钥。
package controller

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	agentconfig "aceitcenter.local/platform/agent/internal/agent"
	"aceitcenter.local/platform/agent/internal/app"
	"aceitcenter.local/platform/internal/core"
)

const maxEnrollmentTokenBytes = 4096

const (
	defaultUpdateInterval = time.Hour
	maximumUpdateJitter   = 10 * time.Minute
	defaultPendingTTL     = 2 * time.Minute
	defaultPairingPoll    = 5 * time.Second
)

// EnrollmentResult 是 enrollment 成功后返回的凭据材料，不能发送给状态或 IPC 响应。
type EnrollmentResult struct {
	NodeID     string
	Credential string
}

// Enroller 执行远程 enrollment 请求。
type Enroller interface {
	Enroll(context.Context, string, string) (EnrollmentResult, error)
}

// PairingStartResult 是创建配对请求后需要持久化的受限材料。
type PairingStartResult struct {
	PairingID  string
	Credential string
	ExpiresAt  time.Time
	PollAfter  time.Duration
}

// Pairer 创建并轮询需要用户在平台确认的配对请求。
type Pairer interface {
	StartPairing(context.Context, string) (PairingStartResult, error)
	PollPairing(context.Context, agentconfig.PendingPairing) (core.PairingPollResult, error)
}

// Worker 对齐 app.Worker，在 context 取消前运行后台 Agent 任务。
type Worker interface {
	Run(context.Context, agentconfig.Config, time.Duration) error
}

// Status 可安全地通过本地 IPC 协议暴露。
type Status struct {
	State         string    `json:"state"`
	NodeID        string    `json:"node_id,omitempty"`
	ServerURL     string    `json:"server_url,omitempty"`
	Version       string    `json:"version,omitempty"`
	LastHeartbeat time.Time `json:"last_heartbeat,omitempty"`
	Error         string    `json:"error,omitempty"`
}

// UpdateStatus 描述已完成的更新检查，且不包含凭据。
type UpdateStatus struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	URL       string `json:"url,omitempty"`
}

// UpdateLaunchAuthorizer 在当前配置 generation 下串行化 helper launch 与配置切换。
type UpdateLaunchAuthorizer func() (finish func(UpdateStatus, bool), err error)

// Dependencies 是 Controller 使用的平台相关操作。
type Dependencies struct {
	ConfigPath          string
	LoadConfig          func() (agentconfig.Config, error)
	PreflightConfig     func() error
	SaveConfig          func(agentconfig.Config) error
	LogBootstrapFailure func()
	Enroller            Enroller
	Pairer              Pairer
	Worker              Worker
	HeartbeatInterval   time.Duration
	CheckUpdate         func(context.Context) (UpdateStatus, error)
	RunUpdate           func(context.Context, agentconfig.Config, UpdateLaunchAuthorizer) (UpdateStatus, error)
	UpdateInterval      time.Duration
	UpdateJitter        func(time.Duration) time.Duration
	Now                 func() time.Time
	UpdatePendingTTL    time.Duration
	PairingPollInterval time.Duration
	CreateDiagnostics   func(context.Context, Status) (string, error)
}

type updateFlight struct {
	done       chan struct{}
	generation uint64
	result     UpdateStatus
	err        error
}

type contextGate chan struct{}

func newContextGate() contextGate {
	gate := make(contextGate, 1)
	gate <- struct{}{}
	return gate
}

func (g contextGate) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g:
		return nil
	}
}

func (g contextGate) release() {
	g <- struct{}{}
}

// Controller 串行化生命周期状态，但慢操作不持有状态互斥锁。
type Controller struct {
	dependencies Dependencies

	launchGate     contextGate
	enrollmentMu   sync.Mutex
	mu             sync.Mutex
	config         agentconfig.Config
	status         Status
	workerCancel   context.CancelFunc
	updateCancel   context.CancelFunc
	workerID       uint64
	lifetime       context.Context
	lifetimeCancel context.CancelFunc
	generation     uint64
	generationCtx  context.Context
	generationStop context.CancelFunc
	shuttingDown   bool
	workerGroup    sync.WaitGroup
	updateMu       sync.Mutex
	flight         *updateFlight
	updatePending  bool
	pendingUpdate  UpdateStatus
	pendingAt      time.Time
}

// New 创建 Controller。提供 ConfigPath 且未注入 SaveConfig 时使用安全默认写入器。
func New(dependencies Dependencies) *Controller {
	if dependencies.HeartbeatInterval == 0 {
		dependencies.HeartbeatInterval = 30 * time.Second
	}
	if dependencies.UpdateInterval <= 0 {
		dependencies.UpdateInterval = defaultUpdateInterval
	}
	if dependencies.UpdateJitter == nil {
		dependencies.UpdateJitter = func(maximum time.Duration) time.Duration {
			return time.Duration(rand.Int63n(int64(maximum) + 1))
		}
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	if dependencies.UpdatePendingTTL <= 0 {
		dependencies.UpdatePendingTTL = defaultPendingTTL
	}
	if dependencies.PairingPollInterval <= 0 {
		dependencies.PairingPollInterval = defaultPairingPoll
	}
	if dependencies.PreflightConfig == nil && dependencies.ConfigPath != "" {
		directory := filepath.Dir(dependencies.ConfigPath)
		dependencies.PreflightConfig = func() error {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return fmt.Errorf("create config directory: %w", err)
			}
			return nil
		}
	}
	if dependencies.SaveConfig == nil && dependencies.ConfigPath != "" {
		dependencies.SaveConfig = func(config agentconfig.Config) error {
			return agentconfig.SaveConfig(dependencies.ConfigPath, config)
		}
	}
	if dependencies.LoadConfig == nil && dependencies.ConfigPath != "" {
		dependencies.LoadConfig = func() (agentconfig.Config, error) {
			return agentconfig.LoadConfig(dependencies.ConfigPath)
		}
	}
	lifetime, cancel := context.WithCancel(context.Background())
	generationContext, generationCancel := context.WithCancel(lifetime)
	return &Controller{
		dependencies:   dependencies,
		launchGate:     newContextGate(),
		lifetime:       lifetime,
		lifetimeCancel: cancel,
		generation:     1,
		generationCtx:  generationContext,
		generationStop: generationCancel,
		status:         Status{State: "stopped"},
	}
}

// Bootstrap 恢复已保存配置；首次安装时保持 waiting，损坏或无权读取的配置进入 degraded。
func (c *Controller) Bootstrap(ctx context.Context) error {
	if ctx == nil {
		return configurationError("service context is required")
	}
	if err := c.launchGate.acquire(ctx); err != nil {
		return err
	}
	defer c.launchGate.release()
	if c.hasPendingUpdate() {
		return configurationError("cannot bootstrap while an update helper is pending")
	}
	c.mu.Lock()
	if c.shuttingDown {
		c.mu.Unlock()
		return configurationError("controller is shutting down")
	}
	if c.lifetimeCancel != nil {
		c.lifetimeCancel()
	}
	c.lifetime, c.lifetimeCancel = context.WithCancel(ctx)
	c.invalidateGenerationLocked()
	loadConfig := c.dependencies.LoadConfig
	c.mu.Unlock()
	if loadConfig == nil {
		c.setBootstrapStatus("waiting")
		return nil
	}
	config, err := loadConfig()
	if errors.Is(err, os.ErrNotExist) {
		c.setBootstrapStatus("waiting")
		return nil
	}
	if err != nil || (!validConfig(config) && !config.IsPendingPairing()) {
		c.setBootstrapStatus("degraded")
		if c.dependencies.LogBootstrapFailure != nil {
			c.dependencies.LogBootstrapFailure()
		}
		return nil
	}
	if config.IsPendingPairing() {
		c.mu.Lock()
		c.config = config
		c.mu.Unlock()
		if !config.PendingPairing.ExpiresAt.After(c.dependencies.Now()) {
			c.setBootstrapStatus("pairing_expired")
			return nil
		}
		if c.dependencies.Pairer == nil {
			c.setBootstrapStatus("degraded")
			return nil
		}
		c.mu.Lock()
		c.status = Status{State: "pairing_recovery", ServerURL: publicURL(config.PendingPairing.ServerURL)}
		pairingContext := c.generationCtx
		generation := c.generation
		c.mu.Unlock()
		c.startPairingPoll(pairingContext, generation, *config.PendingPairing, c.dependencies.PairingPollInterval)
		return nil
	}
	config.ServerURL = publicURL(config.ServerURL)
	c.mu.Lock()
	c.config = config
	c.status = Status{State: "starting", NodeID: config.NodeID, ServerURL: config.ServerURL}
	c.mu.Unlock()
	c.startWorker(config)
	c.startUpdateScheduler()
	return nil
}

// StartPairing 创建平台确认前的配对请求，先原子持久化 pending 状态后再开始轮询。
func (c *Controller) StartPairing(ctx context.Context, serverURL string) error {
	if ctx == nil {
		return configurationError("pairing context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	serverURL = publicURL(serverURL)
	if serverURL == "" {
		return configurationError("server URL must use HTTP or HTTPS")
	}

	c.enrollmentMu.Lock()
	defer c.enrollmentMu.Unlock()
	if c.hasPendingUpdate() {
		return configurationError("cannot start pairing while an update helper is pending")
	}
	c.mu.Lock()
	shuttingDown := c.shuttingDown
	c.mu.Unlock()
	if shuttingDown {
		return configurationError("controller is shutting down")
	}
	if err := c.preflightConfig(); err != nil {
		return operationError("prepare configuration", err)
	}
	if c.dependencies.Pairer == nil {
		return configurationError("pairing client is required")
	}
	if c.dependencies.SaveConfig == nil {
		return configurationError("config writer is required")
	}

	result, err := c.dependencies.Pairer.StartPairing(ctx, serverURL)
	if err != nil {
		return operationError("start pairing", err)
	}
	if result.PairingID == "" || result.Credential == "" || !result.ExpiresAt.After(c.dependencies.Now()) {
		return configurationError("pairing response is incomplete")
	}
	pending := agentconfig.PendingPairing{
		ServerURL:  serverURL,
		PairingID:  result.PairingID,
		Credential: result.Credential,
		ExpiresAt:  result.ExpiresAt,
	}
	config := agentconfig.Config{PendingPairing: &pending}
	if err := c.launchGate.acquire(ctx); err != nil {
		return err
	}
	defer c.launchGate.release()
	if c.hasPendingUpdate() {
		return configurationError("cannot start pairing while an update helper is pending")
	}
	if err := c.dependencies.SaveConfig(config); err != nil {
		return operationErrorWithSecrets("save pairing configuration", err, result.Credential)
	}

	c.mu.Lock()
	c.activateConfigurationLocked(config)
	c.status = Status{State: "waiting_for_approval", ServerURL: serverURL}
	pairingContext := c.generationCtx
	generation := c.generation
	c.mu.Unlock()
	c.startPairingPoll(pairingContext, generation, pending, result.PollAfter)
	return nil
}

// Shutdown 取消所有由 Controller 启动的 Worker，并等待它们退出。
func (c *Controller) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return configurationError("shutdown context is required")
	}
	if err := c.launchGate.acquire(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	c.shuttingDown = true
	if c.workerCancel != nil {
		c.workerCancel()
	}
	if c.updateCancel != nil {
		c.updateCancel()
	}
	if c.lifetimeCancel != nil {
		c.lifetimeCancel()
	}
	if c.generationStop != nil {
		c.generationStop()
	}
	c.updateMu.Lock()
	c.updateMu.Unlock()
	c.mu.Unlock()
	c.launchGate.release()
	done := make(chan struct{})
	go func() {
		c.workerGroup.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Status 返回最新的安全生命周期状态副本。
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return safeStatus(c.status)
}

// ReportStatus 接收 Worker 快照，仅保留可供本地状态和 IPC 输出的字段。
func (c *Controller) ReportStatus(snapshot app.StatusSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = Status{
		State:         string(snapshot.State),
		NodeID:        snapshot.NodeID,
		ServerURL:     publicURL(snapshot.ServerURL),
		Version:       snapshot.Version,
		LastHeartbeat: snapshot.LastHeartbeat,
		Error:         safeStatusError(snapshot.Error),
	}
}

// Enroll 在启动 Worker 前校验并持久化 Agent 配置。enrollment token 仅作为函数参数使用，
// 方法返回后 Controller 不会保留它。
func (c *Controller) Enroll(ctx context.Context, serverURL, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	serverURL, err := validateEnrollment(serverURL, token)
	if err != nil {
		return err
	}

	c.enrollmentMu.Lock()
	defer c.enrollmentMu.Unlock()
	if c.hasPendingUpdate() {
		return configurationError("cannot enroll while an update helper is pending")
	}
	c.mu.Lock()
	shuttingDown := c.shuttingDown
	c.mu.Unlock()
	if shuttingDown {
		return configurationError("controller is shutting down")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.preflightConfig(); err != nil {
		return operationError("prepare configuration", err)
	}
	if c.dependencies.Enroller == nil {
		return configurationError("enrollment client is required")
	}
	if c.dependencies.SaveConfig == nil {
		return configurationError("config writer is required")
	}

	result, err := c.dependencies.Enroller.Enroll(ctx, serverURL, token)
	if err != nil {
		return operationErrorWithSecrets("enroll agent", err, token)
	}
	if result.NodeID == "" || result.Credential == "" {
		return configurationError("enrollment response is incomplete")
	}
	config := agentconfig.Config{ServerURL: serverURL, NodeID: result.NodeID, Credential: result.Credential}
	if err := c.launchGate.acquire(ctx); err != nil {
		return err
	}
	defer c.launchGate.release()
	if c.hasPendingUpdate() {
		return configurationError("cannot enroll while an update helper is pending")
	}
	if err := c.dependencies.SaveConfig(config); err != nil {
		return operationErrorWithSecrets("save agent configuration", err, token, result.Credential)
	}

	c.mu.Lock()
	c.activateConfigurationLocked(config)
	c.status = Status{State: "starting", NodeID: config.NodeID, ServerURL: config.ServerURL}
	c.mu.Unlock()
	c.startWorker(config)
	c.startUpdateScheduler()
	return nil
}

// RestartWorker 停止现有 Worker 并使用已保存配置启动新的 Worker。
func (c *Controller) RestartWorker(_ context.Context) error {
	c.mu.Lock()
	config := c.config
	shuttingDown := c.shuttingDown
	c.mu.Unlock()
	if shuttingDown {
		return configurationError("controller is shutting down")
	}
	if config.ServerURL == "" || config.NodeID == "" || config.Credential == "" {
		return configurationError("agent is not enrolled")
	}
	c.startWorker(config)
	return nil
}

// CheckUpdate 运行更新检查且不阻塞状态读取。
func (c *Controller) CheckUpdate(ctx context.Context) (UpdateStatus, error) {
	if ctx == nil {
		return UpdateStatus{}, configurationError("update context is required")
	}
	if err := ctx.Err(); err != nil {
		return UpdateStatus{}, err
	}
	if c.dependencies.CheckUpdate == nil && c.dependencies.RunUpdate == nil {
		return UpdateStatus{}, configurationError("update checker is required")
	}

	if err := c.launchGate.acquire(ctx); err != nil {
		return UpdateStatus{}, err
	}
	c.mu.Lock()
	if c.shuttingDown {
		c.mu.Unlock()
		c.launchGate.release()
		return UpdateStatus{}, configurationError("controller is shutting down")
	}
	config := c.config
	generation := c.generation
	operationContext := c.generationCtx
	c.updateMu.Lock()
	if flight := c.flight; flight != nil && flight.generation == generation {
		c.updateMu.Unlock()
		c.mu.Unlock()
		c.launchGate.release()
		return waitForUpdateFlight(ctx, flight)
	}
	if c.updatePending && c.pendingWithinTTL(c.dependencies.Now()) {
		result := c.pendingUpdate
		c.updateMu.Unlock()
		c.mu.Unlock()
		c.launchGate.release()
		return result, nil
	}
	flight := &updateFlight{done: make(chan struct{}), generation: generation}
	c.flight = flight
	c.workerGroup.Add(1)
	c.updateMu.Unlock()
	c.mu.Unlock()
	c.launchGate.release()

	go c.executeUpdateFlight(operationContext, config, flight, c.authorizeUpdateLaunch(generation))
	return waitForUpdateFlight(ctx, flight)
}

func (c *Controller) executeUpdateFlight(ctx context.Context, config agentconfig.Config, flight *updateFlight, authorize UpdateLaunchAuthorizer) {
	defer c.workerGroup.Done()
	var result UpdateStatus
	var err error
	if c.dependencies.RunUpdate != nil {
		result, err = c.dependencies.RunUpdate(ctx, config, authorize)
	} else {
		result, err = c.dependencies.CheckUpdate(ctx)
	}
	result.URL = publicURL(result.URL)
	if err != nil {
		err = operationErrorWithSecrets("check for updates", err, config.Credential)
	}
	_ = c.launchGate.acquire(context.Background())
	c.mu.Lock()
	currentGeneration := c.generation
	c.updateMu.Lock()
	if currentGeneration != flight.generation {
		result = UpdateStatus{}
		err = configurationError("update configuration changed")
	}
	flight.result = result
	flight.err = err
	if c.flight == flight {
		c.flight = nil
	}
	close(flight.done)
	c.updateMu.Unlock()
	c.mu.Unlock()
	c.launchGate.release()
}

func (c *Controller) authorizeUpdateLaunch(generation uint64) UpdateLaunchAuthorizer {
	return func() (func(UpdateStatus, bool), error) {
		_ = c.launchGate.acquire(context.Background())
		c.mu.Lock()
		valid := !c.shuttingDown && c.generation == generation
		if !valid {
			c.mu.Unlock()
			c.launchGate.release()
			return nil, configurationError("update configuration changed before helper launch")
		}
		c.updateMu.Lock()
		if c.updatePending && c.pendingWithinTTL(c.dependencies.Now()) {
			c.updateMu.Unlock()
			c.mu.Unlock()
			c.launchGate.release()
			return nil, configurationError("an update helper is already pending")
		}
		c.updateMu.Unlock()
		c.mu.Unlock()

		var once sync.Once
		return func(status UpdateStatus, launched bool) {
			once.Do(func() {
				if launched {
					status.URL = publicURL(status.URL)
					c.mu.Lock()
					c.status.State = "updating"
					c.status.Error = ""
					c.mu.Unlock()
					c.updateMu.Lock()
					c.updatePending = true
					c.pendingUpdate = status
					c.pendingAt = c.dependencies.Now()
					c.updateMu.Unlock()
				}
				c.launchGate.release()
			})
		}, nil
	}
}

func waitForUpdateFlight(ctx context.Context, flight *updateFlight) (UpdateStatus, error) {
	select {
	case <-ctx.Done():
		return UpdateStatus{}, ctx.Err()
	case <-flight.done:
		return flight.result, flight.err
	}
}

// CreateDiagnostics 使用安全状态快照创建诊断包。
func (c *Controller) CreateDiagnostics(ctx context.Context) (string, error) {
	if c.dependencies.CreateDiagnostics == nil {
		return "", configurationError("diagnostics creator is required")
	}
	path, err := c.dependencies.CreateDiagnostics(ctx, c.Status())
	if err != nil {
		return "", operationErrorWithSecrets("create diagnostics", err, c.credential())
	}
	return path, nil
}

func (c *Controller) preflightConfig() error {
	if c.dependencies.PreflightConfig == nil {
		return configurationError("config preflight is required")
	}
	return c.dependencies.PreflightConfig()
}

func (c *Controller) startPairingPoll(ctx context.Context, generation uint64, pending agentconfig.PendingPairing, pollAfter time.Duration) {
	if pollAfter <= 0 {
		pollAfter = defaultPairingPoll
	}
	c.workerGroup.Add(1)
	go func() {
		defer c.workerGroup.Done()
		timer := time.NewTimer(pollAfter)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}

			if !pending.ExpiresAt.After(c.dependencies.Now()) {
				c.setPairingTerminalState(generation, pending.PairingID, "pairing_expired")
				return
			}
			result, err := c.dependencies.Pairer.PollPairing(ctx, pending)
			if err != nil {
				if errors.Is(err, agentconfig.ErrPairingRejected) {
					c.setPairingTerminalState(generation, pending.PairingID, "pairing_rejected")
					return
				}
				if errors.Is(err, core.ErrPairingExpired) {
					c.setPairingTerminalState(generation, pending.PairingID, "pairing_expired")
					return
				}
				// Transient network failures deliberately retain the pending request.
				timer.Reset(pollAfter)
				continue
			}
			switch result.State {
			case core.PairingApproved:
				if c.approvePairing(ctx, generation, pending, result) {
					return
				}
			case core.PairingRejected:
				c.setPairingTerminalState(generation, pending.PairingID, "pairing_rejected")
				return
			case core.PairingExpired:
				c.setPairingTerminalState(generation, pending.PairingID, "pairing_expired")
				return
			}
			timer.Reset(pollAfter)
		}
	}()
}

func (c *Controller) approvePairing(ctx context.Context, generation uint64, pending agentconfig.PendingPairing, result core.PairingPollResult) bool {
	if result.Node == nil || result.Node.ID == "" {
		return false
	}
	if err := c.launchGate.acquire(ctx); err != nil {
		return true
	}
	defer c.launchGate.release()
	c.mu.Lock()
	current := c.generation == generation && c.config.PendingPairing != nil && c.config.PendingPairing.PairingID == pending.PairingID && !c.shuttingDown
	c.mu.Unlock()
	if !current {
		return true
	}
	config := agentconfig.Config{ServerURL: pending.ServerURL, NodeID: result.Node.ID, Credential: pending.Credential}
	if err := c.dependencies.SaveConfig(config); err != nil {
		c.setPairingTerminalState(generation, pending.PairingID, "error")
		return true
	}
	c.mu.Lock()
	if c.generation != generation || c.config.PendingPairing == nil || c.config.PendingPairing.PairingID != pending.PairingID || c.shuttingDown {
		c.mu.Unlock()
		return true
	}
	c.activateConfigurationLocked(config)
	c.status = Status{State: "starting", NodeID: config.NodeID, ServerURL: config.ServerURL}
	c.mu.Unlock()
	c.startWorker(config)
	c.startUpdateScheduler()
	return true
}

func (c *Controller) setPairingTerminalState(generation uint64, pairingID, state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation || c.config.PendingPairing == nil || c.config.PendingPairing.PairingID != pairingID || c.shuttingDown {
		return
	}
	c.status = Status{State: state, ServerURL: publicURL(c.config.PendingPairing.ServerURL)}
}

func (c *Controller) startWorker(config agentconfig.Config) {
	c.mu.Lock()
	if c.shuttingDown {
		c.mu.Unlock()
		return
	}
	if c.workerCancel != nil {
		c.workerCancel()
	}
	worker := c.dependencies.Worker
	if worker == nil {
		c.status.State = "stopped"
		c.workerCancel = nil
		c.mu.Unlock()
		return
	}
	workerContext, cancel := context.WithCancel(c.lifetime)
	c.workerCancel = cancel
	c.workerID++
	workerID := c.workerID
	c.status.State = "starting"
	c.workerGroup.Add(1)
	c.mu.Unlock()

	go func() {
		defer c.workerGroup.Done()
		err := worker.Run(workerContext, config, c.dependencies.HeartbeatInterval)
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.workerID != workerID {
			return
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			c.status.State = "error"
			c.status.Error = safeStatusError(err.Error())
			return
		}
		c.status.State = "stopped"
	}()
}

func (c *Controller) startUpdateScheduler() {
	if c.dependencies.CheckUpdate == nil && c.dependencies.RunUpdate == nil {
		return
	}
	c.mu.Lock()
	if c.shuttingDown {
		c.mu.Unlock()
		return
	}
	if c.updateCancel != nil {
		c.updateCancel()
	}
	schedulerContext, cancel := context.WithCancel(c.generationCtx)
	c.updateCancel = cancel
	c.workerGroup.Add(1)
	c.mu.Unlock()

	go func() {
		defer c.workerGroup.Done()
		_, _ = c.CheckUpdate(schedulerContext)
		for {
			jitter := c.dependencies.UpdateJitter(maximumUpdateJitter)
			if jitter < 0 {
				jitter = 0
			}
			if jitter > maximumUpdateJitter {
				jitter = maximumUpdateJitter
			}
			timer := time.NewTimer(c.dependencies.UpdateInterval + jitter)
			select {
			case <-schedulerContext.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
				_, _ = c.CheckUpdate(schedulerContext)
			}
		}
	}()
}

func (c *Controller) pendingWithinTTL(now time.Time) bool {
	return now.Before(c.pendingAt.Add(c.dependencies.UpdatePendingTTL))
}

func (c *Controller) hasPendingUpdate() bool {
	c.updateMu.Lock()
	defer c.updateMu.Unlock()
	if !c.updatePending {
		return false
	}
	if c.pendingWithinTTL(c.dependencies.Now()) {
		return true
	}
	c.updatePending = false
	c.pendingUpdate = UpdateStatus{}
	c.pendingAt = time.Time{}
	return false
}

// invalidateGenerationLocked 要求 launchGate 与 mu 写锁均已持有。
func (c *Controller) invalidateGenerationLocked() {
	if c.updateCancel != nil {
		c.updateCancel()
		c.updateCancel = nil
	}
	if c.generationStop != nil {
		c.generationStop()
	}
	c.generation++
	c.generationCtx, c.generationStop = context.WithCancel(c.lifetime)
	c.config = agentconfig.Config{}
	c.updateMu.Lock()
	if c.flight != nil && c.flight.generation != c.generation {
		c.flight = nil
	}
	c.updatePending = false
	c.pendingUpdate = UpdateStatus{}
	c.pendingAt = time.Time{}
	c.updateMu.Unlock()
}

// activateConfigurationLocked 要求 launchGate 与 mu 写锁均已持有。
func (c *Controller) activateConfigurationLocked(config agentconfig.Config) {
	c.invalidateGenerationLocked()
	c.config = config
}

func (c *Controller) setBootstrapStatus(state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shuttingDown {
		return
	}
	c.status = Status{State: state, Error: map[string]string{"degraded": "configuration unavailable"}[state]}
}

func validConfig(config agentconfig.Config) bool {
	return publicURL(config.ServerURL) != "" && config.NodeID != "" && config.Credential != ""
}

func (c *Controller) credential() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.config.Credential
}

func validateEnrollment(serverURL, token string) (string, error) {
	serverURL = publicURL(serverURL)
	if serverURL == "" {
		return "", configurationError("server URL must use HTTP or HTTPS")
	}
	if len(token) == 0 || len(token) > maxEnrollmentTokenBytes {
		return "", configurationError("enrollment token must be between 1 and 4096 bytes")
	}
	return serverURL, nil
}

func safeStatus(status Status) Status {
	status.ServerURL = publicURL(status.ServerURL)
	status.Error = safeStatusError(status.Error)
	return status
}

func safeStatusError(value string) string {
	if value == "" {
		return ""
	}
	return "agent operation failed"
}

func publicURL(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	projected := &url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: parsed.Path, RawPath: parsed.RawPath}
	return strings.TrimRight(projected.String(), "/")
}

type lifecycleError struct {
	message string
	cause   error
}

func (e *lifecycleError) Error() string { return e.message }

func (e *lifecycleError) Unwrap() error { return e.cause }

func configurationError(message string) error {
	return &lifecycleError{message: message}
}

func operationError(operation string, cause error) error {
	return operationErrorWithSecrets(operation, cause)
}

func operationErrorWithSecrets(operation string, cause error, secrets ...string) error {
	return &lifecycleError{message: fmt.Sprintf("%s: %s", operation, redact(cause.Error(), secrets...)), cause: cause}
}

func redact(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return strings.Join(strings.Fields(value), " ")
}
