package tray

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"aceitcenter.local/platform/agent/internal/controller"
)

func TestWaitingStatusShowsEnrollmentForm(t *testing.T) {
	view := NewPresenter().View(controller.Status{State: "waiting"})

	if !view.ShowEnrollment || view.StatusText != "等待接入" || view.Icon != IconGray {
		t.Fatalf("view = %#v", view)
	}
	if !view.Actions.ConfigureEnrollment || view.Actions.RestartWorker || !view.Actions.ExitTray {
		t.Fatalf("actions = %#v", view.Actions)
	}
	if !view.Actions.CreateDiagnostics {
		t.Fatalf("diagnostics should be available before enrollment: %#v", view.Actions)
	}
}

func TestPresenterMapsLifecycleStatesAndActions(t *testing.T) {
	heartbeat := time.Date(2026, time.July, 27, 9, 8, 7, 0, time.Local)
	tests := []struct {
		state string
		text  string
		icon  Icon
	}{
		{state: "connecting", text: "正在连接", icon: IconYellow},
		{state: "starting", text: "正在连接", icon: IconYellow},
		{state: "online", text: "运行正常", icon: IconGreen},
		{state: "degraded", text: "服务异常", icon: IconRed},
		{state: "error", text: "服务异常", icon: IconRed},
		{state: "updating", text: "正在更新", icon: IconBlue},
		{state: "stopped", text: "服务已停止", icon: IconGray},
	}

	for _, test := range tests {
		t.Run(test.state, func(t *testing.T) {
			view := NewPresenter().View(controller.Status{
				State:         test.state,
				ServerURL:     "https://it.example",
				Version:       "1.2.3",
				NodeID:        "node-1",
				LastHeartbeat: heartbeat,
			})
			if view.StatusText != test.text || view.Icon != test.icon {
				t.Fatalf("View() = %#v", view)
			}
			if view.ServerURL != "https://it.example" || view.Version != "1.2.3" || view.NodeID != "node-1" || view.LastHeartbeat != "2026-07-27 09:08:07" {
				t.Fatalf("details = %#v", view)
			}
			wantEnrolledActions := test.state != "stopped"
			if view.Actions.OpenPlatform != wantEnrolledActions || view.Actions.RestartWorker != wantEnrolledActions || view.Actions.CheckUpdate != wantEnrolledActions {
				t.Fatalf("actions = %#v", view.Actions)
			}
		})
	}
}

func TestOnlineStatusDoesNotExposeCredential(t *testing.T) {
	view := NewPresenter().View(controller.Status{State: "online", ServerURL: "https://it.example", NodeID: "node-1"})
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bytes.ToLower(encoded), []byte("credential")) || bytes.Contains(encoded, []byte("token")) {
		t.Fatal(string(encoded))
	}
}

func TestStoppedStatusDisablesServiceActions(t *testing.T) {
	view := NewPresenter().View(controller.Status{State: "stopped", ServerURL: "https://it.example", NodeID: "node-1"})
	if view.Actions.ConfigureEnrollment || view.Actions.CreateDiagnostics || view.Actions.CheckUpdate || view.Actions.RestartWorker {
		t.Fatalf("actions = %#v", view.Actions)
	}
	if !view.Actions.OpenLogs || !view.Actions.ExitTray {
		t.Fatalf("local actions = %#v", view.Actions)
	}
}

func TestStatusModelPollFailureDisablesOnlineActionsAndRecovers(t *testing.T) {
	model := NewStatusModel()
	online := model.Apply(controller.Status{State: "online", ServerURL: "https://it.example", NodeID: "node-1"})
	if !online.Actions.OpenPlatform || !online.Actions.RestartWorker {
		t.Fatalf("online actions = %#v", online.Actions)
	}

	unavailable := model.PollFailed()
	if unavailable.StatusText != "Service 不可用" || unavailable.Actions.OpenPlatform || unavailable.Actions.ConfigureEnrollment || unavailable.Actions.CreateDiagnostics || unavailable.Actions.CheckUpdate || unavailable.Actions.RestartWorker {
		t.Fatalf("unavailable view = %#v", unavailable)
	}
	if unavailable.ServerURL != "" || unavailable.NodeID != "" {
		t.Fatalf("unavailable details retained stale status: %#v", unavailable)
	}

	recovered := model.Apply(controller.Status{State: "online", ServerURL: "https://it.example", NodeID: "node-2"})
	if recovered.StatusText != "运行正常" || !recovered.Actions.RestartWorker || recovered.NodeID != "node-2" {
		t.Fatalf("recovered view = %#v", recovered)
	}
}

func TestPresenterKeepsErrorAndUpdateStatusWithoutNotificationData(t *testing.T) {
	for _, status := range []controller.Status{
		{State: "error", Error: "agent operation failed"},
		{State: "degraded", Error: "heartbeat timeout"},
		{State: "updating"},
	} {
		encoded, err := json.Marshal(NewPresenter().View(status))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(bytes.ToLower(encoded), []byte("notification")) {
			t.Fatalf("view exposes native notification data: %s", encoded)
		}
	}
}

func TestWindowsRuntimeUsesOnlyInWindowOperationFeedback(t *testing.T) {
	source, err := os.ReadFile("native_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, forbidden := range []string{".ShowInfo(", ".ShowError("} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Windows runtime still calls native notification API %q", forbidden)
		}
	}
	for _, required := range []string{
		"serviceErrorLabel.SetText(view.Error)",
		"showWindowMessage(successMessage, false)",
		"showWindowMessage(message, false)",
		"showWindowMessage(\"诊断包已创建：",
		"result.Path, false)",
		"showWindowMessage(message, true)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Windows runtime is missing in-window feedback %q", required)
		}
	}
}

func TestBackgroundCoordinatorCancelsAndWakesBeforeOrderedCleanup(t *testing.T) {
	wakeObservedCancel := make(chan bool, 1)
	var coordinator *backgroundCoordinator
	coordinator = newBackgroundCoordinator(func() {
		wakeObservedCancel <- coordinatorContextCanceled(coordinator)
	})
	started := make(chan struct{})
	release := make(chan struct{})
	workerDone := make(chan struct{})
	if !coordinator.Go(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		<-release
		close(workerDone)
	}) {
		t.Fatal("coordinator rejected worker before shutdown")
	}
	<-started

	events := make([]string, 0, 4)
	shutdownDone := make(chan struct{})
	go func() {
		coordinator.Shutdown(func() {
			disposeInOrder(
				func() { events = append(events, "notify") },
				func() { events = append(events, "window") },
				func() { events = append(events, "icons") },
				func() { events = append(events, "handles") },
			)
		})
		close(shutdownDone)
	}()

	select {
	case canceled := <-wakeObservedCancel:
		if !canceled {
			t.Fatal("activation waiter was woken before cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("activation waiter was not woken")
	}
	select {
	case <-shutdownDone:
		t.Fatal("cleanup completed while background operation was running")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not complete")
	}
	if !reflect.DeepEqual(events, []string{"notify", "window", "icons", "handles"}) {
		t.Fatalf("cleanup order = %v", events)
	}
	if coordinator.Go(func(context.Context) {}) {
		t.Fatal("coordinator accepted worker after shutdown")
	}
}

func coordinatorContextCanceled(coordinator *backgroundCoordinator) bool {
	return coordinator.Context().Err() != nil
}

func TestActivateExistingUIReportsForegroundFailures(t *testing.T) {
	events := make([]string, 0, 7)
	result := activateExistingUI(
		func() { events = append(events, "show") },
		func() { events = append(events, "restore") },
		func() error { events = append(events, "bring"); return errors.New("bring failed") },
		func() error { events = append(events, "activate"); return errors.New("activate failed") },
		func(string) { events = append(events, "notify") },
	)

	if !result.BringToTopFailed || !result.ActivateFailed {
		t.Fatalf("activation result = %#v", result)
	}
	want := []string{"show", "restore", "bring", "activate", "show", "restore", "notify"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("activation events = %v, want %v", events, want)
	}
}

func TestSignalExistingInstanceReturnsSetEventError(t *testing.T) {
	want := errors.New("set event failed")
	err := signalExistingInstance(func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("signalExistingInstance() error = %v", err)
	}
}

func TestSynchronizeAndWakeQueuesCallbackBeforeWake(t *testing.T) {
	events := make([]string, 0, 3)
	var queued func()
	result, err := synchronizeAndWake(
		func() { events = append(events, "callback") },
		func(callback func()) { events = append(events, "queue"); queued = callback },
		func() error { events = append(events, "wake"); return nil },
		nil,
	)
	if err != nil || result.FallbackUsed {
		t.Fatalf("synchronizeAndWake() = (%#v, %v)", result, err)
	}
	if !reflect.DeepEqual(events, []string{"queue", "wake"}) {
		t.Fatalf("events before callback = %v", events)
	}
	queued()
	if !reflect.DeepEqual(events, []string{"queue", "wake", "callback"}) {
		t.Fatalf("events after callback = %v", events)
	}
}

func TestSynchronizeAndWakeUsesFallbackWhenWakeFails(t *testing.T) {
	events := make([]string, 0, 3)
	result, err := synchronizeAndWake(
		func() {},
		func(func()) { events = append(events, "queue") },
		func() error { events = append(events, "wake"); return errors.New("post failed") },
		func() error { events = append(events, "fallback"); return nil },
	)
	if err != nil || !result.FallbackUsed {
		t.Fatalf("synchronizeAndWake() = (%#v, %v)", result, err)
	}
	if !reflect.DeepEqual(events, []string{"queue", "wake", "fallback"}) {
		t.Fatalf("events = %v", events)
	}
}

func TestSynchronizeAndWakeReturnsWakeAndFallbackErrors(t *testing.T) {
	wakeErr := errors.New("post failed")
	fallbackErr := errors.New("fallback failed")
	_, err := synchronizeAndWake(func() {}, func(func()) {}, func() error { return wakeErr }, func() error { return fallbackErr })
	if !errors.Is(err, wakeErr) || !errors.Is(err, fallbackErr) {
		t.Fatalf("synchronizeAndWake() error = %v", err)
	}
}

func TestShutdownWaiterCompletesBeforeCleanup(t *testing.T) {
	waiter := newShutdownWaiter()
	release := make(chan struct{})
	events := make(chan string, 2)
	waiter.Start(func() { <-release }, func() { events <- "waiter" })
	cleanupDone := make(chan struct{})
	go func() {
		waiter.Wait()
		events <- "cleanup"
		close(cleanupDone)
	}()

	select {
	case <-cleanupDone:
		t.Fatal("cleanup ran before waiter completed")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case <-cleanupDone:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not run")
	}
	first, second := <-events, <-events
	if first != "waiter" || second != "cleanup" {
		t.Fatalf("events = [%s %s]", first, second)
	}
}

func TestPairingFormDefaultsAndValidatesBeforePending(t *testing.T) {
	form := NewPairingForm()
	if form.ServerURL != DefaultServerURL || form.Pending {
		t.Fatalf("form = %#v", form)
	}

	form.ServerURL = "file:///tmp/server"
	if _, err := form.Begin(); err == nil || form.Pending {
		t.Fatalf("invalid URL Begin() err=%v form=%#v", err, form)
	}

	form.ServerURL = "https://it.example/path"
}

func TestPairingFormPreventsDuplicateSubmitAndCompletesWithoutToken(t *testing.T) {
	form := NewPairingForm()
	form.ServerURL = " https://it.example/path/ "

	serverURL, err := form.Begin()
	if err != nil || serverURL != "https://it.example/path" || !form.Pending {
		t.Fatalf("Begin() = (%q, %v), form=%#v", serverURL, err, form)
	}
	if _, err := form.Begin(); err == nil {
		t.Fatal("duplicate Begin() succeeded")
	}

	form.Complete("enrollment failed")
	if form.Pending || form.Error != "enrollment failed" {
		t.Fatalf("Complete(error) form = %#v", form)
	}

	if _, err := form.Begin(); err != nil {
		t.Fatal(err)
	}
	form.Complete("")
	if form.Pending || form.Error != "" {
		t.Fatalf("Complete(success) form = %#v", form)
	}
}

func TestPairingStatesShowActionableMessages(t *testing.T) {
	presenter := NewPresenter()
	tests := []struct {
		state string
		text  string
		icon  Icon
	}{
		{state: "waiting_for_approval", text: "等待平台确认", icon: IconGray},
		{state: "pairing_recovery", text: "正在等待恢复接入确认", icon: IconBlue},
		{state: "pairing_rejected", text: "配对请求被拒绝，请重新接入", icon: IconRed},
		{state: "pairing_expired", text: "配对请求已过期，请重新接入", icon: IconRed},
	}
	for _, test := range tests {
		t.Run(test.state, func(t *testing.T) {
			view := presenter.View(controller.Status{State: test.state})
			if view.StatusText != test.text || view.Icon != test.icon || !view.ShowEnrollment {
				t.Fatalf("view = %#v", view)
			}
		})
	}
}
