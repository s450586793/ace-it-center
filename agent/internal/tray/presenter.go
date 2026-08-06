// Package tray 提供平台无关的托盘展示模型和 Windows 原生实现。
package tray

//go:generate go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -manifest assets_windows.manifest -o assets_windows.syso

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"aceitcenter.local/platform/agent/internal/controller"
)

const (
	DefaultServerURL            = "http://it.ace-station.top:1111"
	defaultNotificationCooldown = 30 * time.Minute
)

// Icon 是托盘生命周期状态使用的颜色语义。
type Icon string

const (
	IconGray   Icon = "gray"
	IconYellow Icon = "yellow"
	IconGreen  Icon = "green"
	IconRed    Icon = "red"
	IconBlue   Icon = "blue"
)

// Actions 描述当前状态允许触发的托盘操作。
type Actions struct {
	OpenPlatform        bool `json:"open_platform"`
	ConfigureEnrollment bool `json:"configure_enrollment"`
	OpenLogs            bool `json:"open_logs"`
	CreateDiagnostics   bool `json:"create_diagnostics"`
	CheckUpdate         bool `json:"check_update"`
	RestartWorker       bool `json:"restart_worker"`
	ExitTray            bool `json:"exit_tray"`
}

// Notification 描述需要通过原生通知区显示的安全消息。
type Notification struct {
	Title   string `json:"title,omitempty"`
	Message string `json:"message,omitempty"`
	Level   string `json:"level,omitempty"`
}

// View 是不包含 token 或 credential 的托盘展示快照。
type View struct {
	StatusText     string       `json:"status_text"`
	Icon           Icon         `json:"icon"`
	ShowEnrollment bool         `json:"show_enrollment"`
	ServerURL      string       `json:"server_url,omitempty"`
	Version        string       `json:"version,omitempty"`
	NodeID         string       `json:"node_id,omitempty"`
	LastHeartbeat  string       `json:"last_heartbeat,omitempty"`
	Error          string       `json:"error,omitempty"`
	Actions        Actions      `json:"actions"`
	Notification   Notification `json:"notification,omitempty"`
}

// Presenter 将 Service 状态映射为中文原生界面模型。
type Presenter struct{}

func NewPresenter() Presenter {
	return Presenter{}
}

// View 创建一个可安全展示的状态快照。
func (Presenter) View(status controller.Status) View {
	serviceAvailable := status.State != "" && status.State != "stopped" && status.State != "unavailable"
	view := View{
		ServerURL: status.ServerURL,
		Version:   status.Version,
		NodeID:    status.NodeID,
		Error:     status.Error,
		Actions: Actions{
			ConfigureEnrollment: serviceAvailable && status.State != "updating",
			OpenLogs:            true,
			ExitTray:            true,
		},
	}
	if !status.LastHeartbeat.IsZero() {
		view.LastHeartbeat = status.LastHeartbeat.Local().Format("2006-01-02 15:04:05")
	}

	switch status.State {
	case "waiting", "waiting_for_enrollment":
		view.StatusText = "等待接入"
		view.Icon = IconGray
		view.ShowEnrollment = true
	case "waiting_for_approval":
		view.StatusText = "等待平台确认"
		view.Icon = IconGray
		view.ShowEnrollment = true
	case "pairing_recovery":
		view.StatusText = "正在等待恢复接入确认"
		view.Icon = IconBlue
		view.ShowEnrollment = true
	case "pairing_rejected":
		view.StatusText = "配对请求被拒绝，请重新接入"
		view.Icon = IconRed
		view.ShowEnrollment = true
	case "pairing_expired":
		view.StatusText = "配对请求已过期，请重新接入"
		view.Icon = IconRed
		view.ShowEnrollment = true
	case "starting", "connecting":
		view.StatusText = "正在连接"
		view.Icon = IconYellow
	case "online":
		view.StatusText = "运行正常"
		view.Icon = IconGreen
	case "degraded", "error":
		view.StatusText = "服务异常"
		view.Icon = IconRed
		view.Notification = Notification{Title: "Ace Agent", Message: status.Error, Level: "error"}
	case "updating":
		view.StatusText = "正在更新"
		view.Icon = IconBlue
		view.Notification = Notification{Title: "Ace Agent", Message: "正在更新 Agent", Level: "info"}
	case "stopped":
		view.StatusText = "服务已停止"
		view.Icon = IconGray
	case "unavailable":
		view.StatusText = "Service 不可用"
		view.Icon = IconGray
	default:
		view.StatusText = "状态未知"
		view.Icon = IconGray
	}

	enrolled := status.ServerURL != "" && status.NodeID != "" && status.State != "stopped"
	view.Actions.OpenPlatform = enrolled
	view.Actions.CreateDiagnostics = serviceAvailable
	view.Actions.CheckUpdate = enrolled
	view.Actions.RestartWorker = enrolled
	return view
}

// StatusModel 保存最新一次状态轮询的展示结果，并在轮询失败时清除陈旧详情。
type StatusModel struct {
	presenter             Presenter
	view                  View
	now                   func() time.Time
	notificationCooldown  time.Duration
	notificationDisplayed map[string]time.Time
}

func NewStatusModel() *StatusModel {
	return newStatusModel(time.Now, defaultNotificationCooldown)
}

func newStatusModel(now func() time.Time, notificationCooldown time.Duration) *StatusModel {
	if now == nil {
		now = time.Now
	}
	if notificationCooldown <= 0 {
		notificationCooldown = defaultNotificationCooldown
	}
	presenter := NewPresenter()
	return &StatusModel{
		presenter:             presenter,
		view:                  presenter.View(controller.Status{State: "unavailable"}),
		now:                   now,
		notificationCooldown:  notificationCooldown,
		notificationDisplayed: make(map[string]time.Time),
	}
}

func (m *StatusModel) Apply(status controller.Status) View {
	m.view = m.presenter.View(status)
	m.filterNotification()
	return m.view
}

func (m *StatusModel) PollFailed() View {
	m.view = m.presenter.View(controller.Status{State: "unavailable"})
	return m.view
}

func (m *StatusModel) filterNotification() {
	notification := m.view.Notification
	if notification.Message == "" {
		return
	}
	key := notification.Level + "\x00" + notification.Message
	now := m.now()
	if displayedAt, exists := m.notificationDisplayed[key]; exists && now.Before(displayedAt.Add(m.notificationCooldown)) {
		m.view.Notification = Notification{}
		return
	}
	m.notificationDisplayed[key] = now
}

type backgroundCoordinator struct {
	ctx    context.Context
	cancel context.CancelFunc
	wake   func()

	mu       sync.Mutex
	stopping bool
	workers  sync.WaitGroup
	cleanup  sync.Once
}

func newBackgroundCoordinator(wake func()) *backgroundCoordinator {
	ctx, cancel := context.WithCancel(context.Background())
	return &backgroundCoordinator{ctx: ctx, cancel: cancel, wake: wake}
}

func (c *backgroundCoordinator) Context() context.Context {
	return c.ctx
}

func (c *backgroundCoordinator) Go(operation func(context.Context)) bool {
	if operation == nil {
		return false
	}
	c.mu.Lock()
	if c.stopping {
		c.mu.Unlock()
		return false
	}
	c.workers.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.workers.Done()
		operation(c.ctx)
	}()
	return true
}

func (c *backgroundCoordinator) stop() bool {
	c.mu.Lock()
	if c.stopping {
		c.mu.Unlock()
		return false
	}
	c.stopping = true
	c.cancel()
	c.mu.Unlock()
	if c.wake != nil {
		c.wake()
	}
	return true
}

func (c *backgroundCoordinator) Wait() {
	c.workers.Wait()
}

func (c *backgroundCoordinator) Shutdown(cleanup func()) {
	c.stop()
	c.Wait()
	if cleanup != nil {
		c.cleanup.Do(cleanup)
	}
}

func disposeInOrder(disposeNotifyIcon, disposeWindow, disposeIcons, closeHandles func()) {
	for _, dispose := range []func(){disposeNotifyIcon, disposeWindow, disposeIcons, closeHandles} {
		if dispose != nil {
			dispose()
		}
	}
}

type activationResult struct {
	BringToTopFailed bool
	ActivateFailed   bool
}

func activateExistingUI(show, restore func(), bringToTop, activate func() error, report func(string)) activationResult {
	show()
	restore()
	result := activationResult{
		BringToTopFailed: bringToTop() != nil,
		ActivateFailed:   activate() != nil,
	}
	if result.BringToTopFailed || result.ActivateFailed {
		show()
		restore()
		if report != nil {
			report("无法将设置窗口切换到前台，请从任务栏打开")
		}
	}
	return result
}

func signalExistingInstance(signal func() error) error {
	if signal == nil {
		return errors.New("tray activation signal is required")
	}
	if err := signal(); err != nil {
		return fmt.Errorf("signal existing tray instance: %w", err)
	}
	return nil
}

type synchronizeWakeResult struct {
	FallbackUsed bool
}

func synchronizeAndWake(callback func(), synchronize func(func()), wake, fallback func() error) (synchronizeWakeResult, error) {
	if callback == nil || synchronize == nil || wake == nil {
		return synchronizeWakeResult{}, errors.New("callback, synchronizer, and wake function are required")
	}
	synchronize(callback)
	wakeErr := wake()
	if wakeErr == nil {
		return synchronizeWakeResult{}, nil
	}
	result := synchronizeWakeResult{FallbackUsed: true}
	if fallback == nil {
		return result, wakeErr
	}
	if fallbackErr := fallback(); fallbackErr != nil {
		return result, errors.Join(wakeErr, fallbackErr)
	}
	return result, nil
}

type shutdownWaiter struct {
	once sync.Once
	done chan struct{}
}

func newShutdownWaiter() *shutdownWaiter {
	return &shutdownWaiter{done: make(chan struct{})}
}

func (w *shutdownWaiter) Start(wait, after func()) {
	w.once.Do(func() {
		go func() {
			defer close(w.done)
			if wait != nil {
				wait()
			}
			if after != nil {
				after()
			}
		}()
	})
}

func (w *shutdownWaiter) Wait() {
	<-w.done
}

// PairingForm 保存原生配对表单的可测试交互状态。
type PairingForm struct {
	ServerURL string
	Error     string
	Pending   bool
}

func NewPairingForm() *PairingForm {
	return &PairingForm{ServerURL: DefaultServerURL}
}

// Begin 校验并锁定一次提交；返回值仅供紧接着的 IPC 请求使用。
func (f *PairingForm) Begin() (string, error) {
	if f.Pending {
		return "", errors.New("配对请求正在处理中")
	}
	serverURL := strings.TrimSpace(f.ServerURL)
	if !validServerURL(serverURL) {
		return "", f.validationError("请输入有效的 HTTP 或 HTTPS 服务地址")
	}
	serverURL = strings.TrimRight(serverURL, "/")
	f.ServerURL = serverURL
	f.Error = ""
	f.Pending = true
	return serverURL, nil
}

func (f *PairingForm) validationError(message string) error {
	f.Pending = false
	f.Error = message
	return errors.New(message)
}

// Complete 结束提交。message 必须来自 IPC ResponseError 或固定成功提示。
func (f *PairingForm) Complete(message string) {
	f.Pending = false
	f.Error = message
}

func validServerURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}
