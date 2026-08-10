//go:build windows

package tray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"aceitcenter.local/platform/agent/internal/controller"
	"aceitcenter.local/platform/agent/internal/ipc"
	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

const defaultRefreshInterval = 10 * time.Second

// Options 配置原生托盘运行时。
type Options struct {
	Dial            func(context.Context) (ipc.Client, error)
	LogDirectory    string
	RefreshInterval time.Duration
	ShowOnStart     bool
}

type trayRuntime struct {
	parent      context.Context
	coordinator *backgroundCoordinator
	waiter      *shutdownWaiter
	options     Options
	statusModel *StatusModel
	clients     clientSource
	form        *PairingForm

	window            *walk.MainWindow
	notifyIcon        *walk.NotifyIcon
	statusLabel       *walk.Label
	serverLabel       *walk.Label
	versionLabel      *walk.Label
	nodeLabel         *walk.Label
	heartbeatLabel    *walk.Label
	serviceErrorLabel *walk.Label
	enrollmentGroup   *walk.GroupBox
	serverEdit        *walk.LineEdit
	messageLabel      *walk.Label
	submitButton      *walk.PushButton
	openPlatformBtn   *walk.PushButton
	diagnosticsBtn    *walk.PushButton
	updateBtn         *walk.PushButton
	restartBtn        *walk.PushButton

	openPlatformAction *walk.Action
	configureAction    *walk.Action
	diagnosticsAction  *walk.Action
	updateAction       *walk.Action
	restartAction      *walk.Action

	icons                  map[Icon]*walk.Icon
	currentView            View
	enrollmentManuallyOpen bool
	refreshing             atomic.Bool
	quitting               atomic.Bool
	lastActivation         activationResult
	uiThreadID             uint32
	shutdownErrMu          sync.Mutex
	shutdownErr            error
}

type clientSource struct {
	mu      sync.Mutex
	initial ipc.Client
	dial    func(context.Context) (ipc.Client, error)
}

func (s *clientSource) next(ctx context.Context) (ipc.Client, error) {
	s.mu.Lock()
	if s.initial != nil {
		client := s.initial
		s.initial = nil
		s.mu.Unlock()
		return client, nil
	}
	dial := s.dial
	s.mu.Unlock()
	if dial == nil {
		return nil, errors.New("IPC dialer is unavailable")
	}
	return dial(ctx)
}

func (s *clientSource) closeInitial() {
	s.mu.Lock()
	client := s.initial
	s.initial = nil
	s.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

// Run 启动当前登录会话中的唯一原生托盘进程。
func Run(ctx context.Context, initial ipc.Client, options Options) error {
	if ctx == nil {
		return errors.New("tray context is required")
	}
	instance, primary, err := acquireSingleInstance()
	if err != nil {
		if initial != nil {
			_ = initial.Close()
		}
		return err
	}
	if !primary {
		if initial != nil {
			_ = initial.Close()
		}
		return nil
	}
	if options.Dial == nil {
		options.Dial = ipc.DialWindows
	}
	if options.RefreshInterval <= 0 {
		options.RefreshInterval = defaultRefreshInterval
	}
	if initial == nil {
		dialContext, cancel := context.WithTimeout(ctx, 3*time.Second)
		initial, _ = options.Dial(dialContext)
		cancel()
	}
	runtime := &trayRuntime{
		parent:      ctx,
		options:     options,
		statusModel: NewStatusModel(),
		clients:     clientSource{initial: initial, dial: options.Dial},
		form:        NewPairingForm(),
		waiter:      newShutdownWaiter(),
	}
	runtime.coordinator = newBackgroundCoordinator(instance.wake)
	cleanup := func() {
		runtime.clients.closeInitial()
		disposeInOrder(runtime.disposeNotifyIcon, runtime.disposeWindow, runtime.disposeIcons, instance.Close)
	}
	if err := runtime.buildWindow(); err != nil {
		runtime.coordinator.Shutdown(cleanup)
		return err
	}
	runtime.uiThreadID = win.GetWindowThreadProcessId(runtime.window.Handle(), nil)
	if runtime.uiThreadID == 0 {
		runtime.coordinator.Shutdown(cleanup)
		return errors.New("locate tray UI thread")
	}
	if err := runtime.buildNotifyIcon(); err != nil {
		runtime.coordinator.Shutdown(cleanup)
		return err
	}
	runtime.applyView(runtime.statusModel.PollFailed())
	if options.ShowOnStart {
		runtime.showWindow()
	}

	runtime.coordinator.Go(runtime.watchContext)
	runtime.coordinator.Go(func(ctx context.Context) { runtime.watchActivation(ctx, instance) })
	runtime.coordinator.Go(func(ctx context.Context) { runtime.watchUpdate(ctx, instance) })
	runtime.refreshStatus(true)
	returnCode := runtime.window.Run()
	runtime.beginShutdown()
	runtime.waiter.Wait()
	runtime.coordinator.Shutdown(cleanup)
	if err := runtime.shutdownError(); err != nil {
		return err
	}
	if returnCode != 0 && ctx.Err() == nil {
		return fmt.Errorf("tray message loop exited with code %d", returnCode)
	}
	return nil
}

func (r *trayRuntime) buildWindow() error {
	err := (MainWindow{
		AssignTo: &r.window,
		Title:    "Ace IT Center Agent",
		Size:     Size{Width: 520, Height: 500},
		MinSize:  Size{Width: 480, Height: 460},
		Visible:  false,
		Layout:   VBox{Margins: Margins{Left: 18, Top: 16, Right: 18, Bottom: 16}, Spacing: 10},
		Children: []Widget{
			Label{Text: "Agent 状态", Font: Font{Bold: true, PointSize: 13}},
			Label{AssignTo: &r.statusLabel, Text: "正在读取 Service 状态..."},
			Label{AssignTo: &r.serverLabel, Text: "服务器：-"},
			Label{AssignTo: &r.versionLabel, Text: "版本：-"},
			Label{AssignTo: &r.nodeLabel, Text: "节点：-"},
			Label{AssignTo: &r.heartbeatLabel, Text: "最近心跳：-"},
			Label{AssignTo: &r.serviceErrorLabel, TextColor: walk.RGB(180, 35, 35)},
			GroupBox{
				AssignTo: &r.enrollmentGroup,
				Title:    "接入 Ace IT Center",
				Visible:  false,
				Layout:   Grid{Columns: 2, Spacing: 8},
				Children: []Widget{
					Label{Text: "服务器"},
					LineEdit{AssignTo: &r.serverEdit, Text: DefaultServerURL},
					HSpacer{},
					PushButton{AssignTo: &r.submitButton, Text: "接入", OnClicked: r.submitPairing},
				},
			},
			Label{AssignTo: &r.messageLabel},
			Composite{
				Layout: HBox{Spacing: 8},
				Children: []Widget{
					PushButton{AssignTo: &r.openPlatformBtn, Text: "打开平台", OnClicked: r.openPlatform},
					PushButton{Text: "打开日志", OnClicked: r.openLogs},
					PushButton{AssignTo: &r.diagnosticsBtn, Text: "创建诊断", OnClicked: r.createDiagnostics},
				},
			},
			Composite{
				Layout: HBox{Spacing: 8},
				Children: []Widget{
					PushButton{AssignTo: &r.updateBtn, Text: "检查更新", OnClicked: r.checkUpdate},
					PushButton{AssignTo: &r.restartBtn, Text: "重启 Worker", OnClicked: r.restartWorker},
					HSpacer{},
					PushButton{Text: "隐藏", OnClicked: func() { r.window.Hide() }},
				},
			},
		},
	}).Create()
	if err != nil {
		return fmt.Errorf("create tray settings window: %w", err)
	}
	r.window.Closing().Attach(func(canceled *bool, _ walk.CloseReason) {
		if !r.quitting.Load() {
			*canceled = true
			r.window.Hide()
		}
	})
	return nil
}

func (r *trayRuntime) buildNotifyIcon() error {
	icons, err := makeStatusIcons()
	if err != nil {
		return err
	}
	r.icons = icons
	if err := r.window.SetIcon(icons[IconGreen]); err != nil {
		return fmt.Errorf("set settings icon: %w", err)
	}
	r.notifyIcon, err = walk.NewNotifyIcon(r.window)
	if err != nil {
		return fmt.Errorf("create notification icon: %w", err)
	}
	if err := r.notifyIcon.SetIcon(icons[IconGray]); err != nil {
		return fmt.Errorf("set notification icon: %w", err)
	}
	if err := r.notifyIcon.SetToolTip("Ace IT Center Agent"); err != nil {
		return fmt.Errorf("set notification tooltip: %w", err)
	}
	r.notifyIcon.MouseDown().Attach(func(_, _ int, button walk.MouseButton) {
		if button == walk.LeftButton {
			r.showWindow()
		}
	})

	if err := r.addTrayActions(); err != nil {
		return err
	}
	if err := r.notifyIcon.SetVisible(true); err != nil {
		return fmt.Errorf("show notification icon: %w", err)
	}
	return nil
}

func (r *trayRuntime) addTrayActions() error {
	type actionSpec struct {
		text    string
		handler func()
		assign  **walk.Action
	}
	specs := []actionSpec{
		{text: "打开设置", handler: r.showWindow},
		{text: "打开 Ace IT Center", handler: r.openPlatform, assign: &r.openPlatformAction},
		{text: "配置接入", handler: r.configureEnrollment, assign: &r.configureAction},
		{text: "打开日志目录", handler: r.openLogs},
		{text: "创建诊断包", handler: r.createDiagnostics, assign: &r.diagnosticsAction},
		{text: "检查更新", handler: r.checkUpdate, assign: &r.updateAction},
		{text: "重启 Worker", handler: r.restartWorker, assign: &r.restartAction},
		{text: "退出托盘", handler: r.exitTray},
	}
	for _, spec := range specs {
		action := walk.NewAction()
		if err := action.SetText(spec.text); err != nil {
			return fmt.Errorf("set tray action text: %w", err)
		}
		action.Triggered().Attach(spec.handler)
		if err := r.notifyIcon.ContextMenu().Actions().Add(action); err != nil {
			return fmt.Errorf("add tray action: %w", err)
		}
		if spec.assign != nil {
			*spec.assign = action
		}
	}
	return nil
}

func (r *trayRuntime) refreshStatus(showOnFailure bool) {
	if !r.refreshing.CompareAndSwap(false, true) {
		return
	}
	started := r.coordinator.Go(func(ctx context.Context) {
		defer r.refreshing.Store(false)
		operationContext, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		var status controller.Status
		err := r.call(operationContext, "status.get", nil, &status)
		r.window.Synchronize(func() {
			if err != nil {
				r.applyView(r.statusModel.PollFailed())
				r.showSafeError(userFacingError(err))
				if showOnFailure {
					r.showWindow()
				}
				return
			}
			r.applyView(r.statusModel.Apply(status))
			if showOnFailure && (status.State == "waiting" || status.State == "waiting_for_enrollment" || status.State == "pairing_rejected" || status.State == "pairing_expired") {
				r.showWindow()
			}
		})
	})
	if !started {
		r.refreshing.Store(false)
	}
}

func (r *trayRuntime) applyView(view View) {
	r.currentView = view
	_ = r.statusLabel.SetText("Service：" + view.StatusText)
	_ = r.serverLabel.SetText("服务器：" + displayValue(view.ServerURL))
	_ = r.versionLabel.SetText("版本：" + displayValue(view.Version))
	_ = r.nodeLabel.SetText("节点：" + displayValue(view.NodeID))
	_ = r.heartbeatLabel.SetText("最近心跳：" + displayValue(view.LastHeartbeat))
	_ = r.serviceErrorLabel.SetText(view.Error)
	if icon := r.icons[view.Icon]; icon != nil {
		_ = r.notifyIcon.SetIcon(icon)
	}
	_ = r.notifyIcon.SetToolTip("Ace Agent - " + view.StatusText)
	r.enrollmentGroup.SetVisible(view.ShowEnrollment || r.enrollmentManuallyOpen)
	_ = r.openPlatformAction.SetEnabled(view.Actions.OpenPlatform)
	_ = r.configureAction.SetEnabled(view.Actions.ConfigureEnrollment)
	_ = r.diagnosticsAction.SetEnabled(view.Actions.CreateDiagnostics)
	_ = r.updateAction.SetEnabled(view.Actions.CheckUpdate)
	_ = r.restartAction.SetEnabled(view.Actions.RestartWorker)
	r.openPlatformBtn.SetEnabled(view.Actions.OpenPlatform)
	r.diagnosticsBtn.SetEnabled(view.Actions.CreateDiagnostics)
	r.updateBtn.SetEnabled(view.Actions.CheckUpdate)
	r.restartBtn.SetEnabled(view.Actions.RestartWorker)
	r.updateEnrollmentControls()
}

func (r *trayRuntime) submitPairing() {
	r.form.ServerURL = r.serverEdit.Text()
	serverURL, err := r.form.Begin()
	if err != nil {
		r.updateEnrollmentControls()
		r.showWindowMessage(r.form.Error, true)
		return
	}
	r.updateEnrollmentControls()
	r.clearWindowMessage()
	started := r.coordinator.Go(func(ctx context.Context) {
		operationContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		params := struct {
			ServerURL string `json:"server_url"`
		}{ServerURL: serverURL}
		var status controller.Status
		err := r.call(operationContext, "pairing.start", params, &status)
		message := ""
		if err != nil {
			message = userFacingError(err)
		}
		r.window.Synchronize(func() {
			r.form.Complete(message)
			r.updateEnrollmentControls()
			if err != nil {
				r.showWindowMessage(message, true)
				return
			}
			r.enrollmentManuallyOpen = false
			r.applyView(r.statusModel.Apply(status))
			r.showWindowMessage("已发送配对请求，请在平台确认", false)
		})
	})
	if !started {
		r.form.Complete("")
		r.updateEnrollmentControls()
	}
}

func (r *trayRuntime) updateEnrollmentControls() {
	enabled := r.currentView.Actions.ConfigureEnrollment && !r.form.Pending
	r.submitButton.SetEnabled(enabled)
	r.serverEdit.SetEnabled(enabled)
}

func (r *trayRuntime) restartWorker() {
	r.callStatusAction("worker.restart", "Worker 已重新启动")
}

func (r *trayRuntime) callStatusAction(method, successMessage string) {
	r.coordinator.Go(func(ctx context.Context) {
		operationContext, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		var status controller.Status
		err := r.call(operationContext, method, nil, &status)
		r.window.Synchronize(func() {
			if err != nil {
				r.showSafeError(userFacingError(err))
				return
			}
			r.applyView(r.statusModel.Apply(status))
			r.showWindowMessage(successMessage, false)
		})
	})
}

func (r *trayRuntime) checkUpdate() {
	r.coordinator.Go(func(ctx context.Context) {
		operationContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var result controller.UpdateStatus
		err := r.call(operationContext, "update.check", nil, &result)
		r.window.Synchronize(func() {
			if err != nil {
				r.showSafeError(userFacingError(err))
				return
			}
			message := "当前已是最新版本"
			if result.Available {
				message = "发现新版本 " + displayValue(result.Version)
			}
			r.showWindowMessage(message, false)
		})
	})
}

func (r *trayRuntime) createDiagnostics() {
	r.coordinator.Go(func(ctx context.Context) {
		operationContext, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		var result struct {
			Path string `json:"path"`
		}
		err := r.call(operationContext, "diagnostics.create", nil, &result)
		r.window.Synchronize(func() {
			if err != nil {
				r.showSafeError(userFacingError(err))
				return
			}
			r.showWindowMessage("诊断包已创建："+result.Path, false)
		})
	})
}

func (r *trayRuntime) configureEnrollment() {
	r.enrollmentManuallyOpen = true
	r.enrollmentGroup.SetVisible(true)
	r.showWindow()
	_ = r.serverEdit.SetFocus()
}

func (r *trayRuntime) openPlatform() {
	if r.currentView.ServerURL == "" {
		return
	}
	if err := shellOpen(r.currentView.ServerURL); err != nil {
		r.showSafeError("无法打开 Ace IT Center")
	}
}

func (r *trayRuntime) openLogs() {
	directory := r.options.LogDirectory
	if directory == "" {
		programData := os.Getenv("ProgramData")
		if programData == "" {
			programData = `C:\ProgramData`
		}
		directory = filepath.Join(programData, "AceITCenter", "logs")
	}
	if err := shellOpen(directory); err != nil {
		r.showSafeError("无法打开日志目录")
	}
}

func (r *trayRuntime) showWindow() {
	r.lastActivation = activateExistingUI(
		r.window.Show,
		func() { win.ShowWindow(r.window.Handle(), win.SW_RESTORE) },
		r.window.BringToTop,
		r.window.Activate,
		r.showSafeError,
	)
}

func (r *trayRuntime) showSafeError(message string) {
	r.showWindowMessage(message, true)
}

func (r *trayRuntime) showWindowMessage(message string, isError bool) {
	if message == "" {
		return
	}
	color := walk.RGB(55, 65, 81)
	if isError {
		color = walk.RGB(180, 35, 35)
	}
	r.messageLabel.SetTextColor(color)
	_ = r.messageLabel.SetText(message)
}

func (r *trayRuntime) clearWindowMessage() {
	_ = r.messageLabel.SetText("")
}

func (r *trayRuntime) exitTray() {
	r.requestShutdown()
}

func (r *trayRuntime) beginShutdown() {
	if !r.coordinator.stop() {
		return
	}
	r.quitting.Store(true)
	r.waiter.Start(r.coordinator.Wait, r.requestAppExit)
}

func (r *trayRuntime) requestShutdown() {
	_, err := synchronizeAndWake(
		r.beginShutdown,
		r.window.Synchronize,
		r.wakeWindow,
		func() error {
			r.beginShutdown()
			return nil
		},
	)
	if err != nil {
		r.recordShutdownError(fmt.Errorf("schedule tray shutdown: %w", err))
		r.beginShutdown()
	}
}

func (r *trayRuntime) requestAppExit() {
	_, err := synchronizeAndWake(
		func() { walk.App().Exit(0) },
		r.window.Synchronize,
		r.wakeWindow,
		func() error { return postThreadQuit(r.uiThreadID) },
	)
	if err != nil {
		r.recordShutdownError(fmt.Errorf("wake tray UI for exit: %w", err))
	}
}

func (r *trayRuntime) wakeWindow() error {
	if win.PostMessage(r.window.Handle(), win.WM_NULL, 0, 0) == 0 {
		return errors.New("post WM_NULL to tray window")
	}
	return nil
}

func (r *trayRuntime) recordShutdownError(err error) {
	if err == nil {
		return
	}
	r.shutdownErrMu.Lock()
	r.shutdownErr = errors.Join(r.shutdownErr, err)
	r.shutdownErrMu.Unlock()
}

func (r *trayRuntime) shutdownError() error {
	r.shutdownErrMu.Lock()
	defer r.shutdownErrMu.Unlock()
	return r.shutdownErr
}

func (r *trayRuntime) watchContext(ctx context.Context) {
	ticker := time.NewTicker(r.options.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.parent.Done():
			r.requestShutdown()
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.refreshStatus(false)
		}
	}
}

func (r *trayRuntime) watchActivation(ctx context.Context, instance *singleInstance) {
	for instance.waitForActivation(ctx) {
		r.window.Synchronize(func() {
			if ctx.Err() == nil {
				r.showWindow()
			}
		})
	}
}

func (r *trayRuntime) watchUpdate(ctx context.Context, instance *singleInstance) {
	if instance.waitForUpdate(ctx) {
		r.requestShutdown()
	}
}

func (r *trayRuntime) call(ctx context.Context, method string, params any, result any) error {
	client, err := r.clients.next(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	var encoded json.RawMessage
	if params != nil {
		encoded, err = json.Marshal(params)
		if err != nil {
			return errors.New("encode IPC parameters")
		}
	}
	response, err := client.Call(ctx, ipc.Request{ID: nextRequestID(), Method: method, Params: encoded})
	if err != nil {
		return err
	}
	if response.Error != nil {
		return &serviceResponseError{message: response.Error.Message}
	}
	if result == nil {
		return nil
	}
	contents, err := json.Marshal(response.Result)
	if err != nil || json.Unmarshal(contents, result) != nil {
		return errors.New("invalid IPC response")
	}
	return nil
}

func (r *trayRuntime) disposeNotifyIcon() {
	if r.notifyIcon != nil {
		_ = r.notifyIcon.SetVisible(false)
		r.notifyIcon.Dispose()
	}
}

func (r *trayRuntime) disposeWindow() {
	if r.window != nil {
		r.window.Dispose()
	}
}

func (r *trayRuntime) disposeIcons() {
	for _, icon := range r.icons {
		icon.Dispose()
	}
}

type serviceResponseError struct {
	message string
}

func (e *serviceResponseError) Error() string {
	return e.message
}

func userFacingError(err error) string {
	var responseError *serviceResponseError
	if errors.As(err, &responseError) && responseError.message != "" {
		return responseError.message
	}
	return "无法连接到 Ace Agent Service"
}

var requestSequence atomic.Uint64

func nextRequestID() string {
	return fmt.Sprintf("tray-%d", requestSequence.Add(1))
}

func displayValue(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func makeStatusIcons() (map[Icon]*walk.Icon, error) {
	colors := map[Icon]color.RGBA{
		IconGray:   {R: 110, G: 118, B: 126, A: 255},
		IconYellow: {R: 224, G: 153, B: 0, A: 255},
		IconGreen:  {R: 34, G: 139, B: 94, A: 255},
		IconRed:    {R: 205, G: 54, B: 54, A: 255},
		IconBlue:   {R: 42, G: 111, B: 200, A: 255},
	}
	icons := make(map[Icon]*walk.Icon, len(colors))
	for state, fill := range colors {
		icon, err := walk.NewIconFromImage(statusImage(fill))
		if err != nil {
			for _, created := range icons {
				created.Dispose()
			}
			return nil, fmt.Errorf("create %s tray icon: %w", state, err)
		}
		icons[state] = icon
	}
	return icons, nil
}

func statusImage(fill color.RGBA) image.Image {
	const size = 32
	imageValue := image.NewRGBA(image.Rect(0, 0, size, size))
	center := size / 2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := x-center, y-center
			distance := dx*dx + dy*dy
			switch {
			case distance <= 13*13:
				imageValue.SetRGBA(x, y, fill)
			case distance <= 15*15:
				imageValue.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			}
		}
	}
	return imageValue
}

var (
	kernel32      = windows.NewLazySystemDLL("kernel32.dll")
	attachConsole = kernel32.NewProc("AttachConsole")
	shell32       = windows.NewLazySystemDLL("shell32.dll")
	shellExecute  = shell32.NewProc("ShellExecuteW")
	user32        = windows.NewLazySystemDLL("user32.dll")
	postThreadMsg = user32.NewProc("PostThreadMessageW")
)

func postThreadQuit(threadID uint32) error {
	if threadID == 0 {
		return errors.New("tray UI thread is unavailable")
	}
	result, _, callErr := postThreadMsg.Call(uintptr(threadID), uintptr(win.WM_QUIT), 0, 0)
	if result == 0 {
		return fmt.Errorf("PostThreadMessageW(WM_QUIT) failed: %v", callErr)
	}
	return nil
}

// AttachParentConsole 仅为显式 CLI mode 尝试复用父进程 console。
func AttachParentConsole() bool {
	const attachParentProcess = ^uint32(0)
	attached, _, _ := attachConsole.Call(uintptr(attachParentProcess))
	if attached == 0 {
		return false
	}
	if stdout, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = stdout
	}
	if stderr, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stderr = stderr
	}
	if stdin, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = stdin
	}
	return true
}

func shellOpen(target string) error {
	if target == "" {
		return errors.New("shell target is required")
	}
	operation, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	result, _, callErr := shellExecute.Call(0, uintptr(unsafePointer(operation)), uintptr(unsafePointer(file)), 0, 0, 1)
	if result <= 32 {
		return fmt.Errorf("ShellExecuteW failed: %w", callErr)
	}
	return nil
}

func unsafePointer(value *uint16) unsafe.Pointer {
	return unsafe.Pointer(value)
}
