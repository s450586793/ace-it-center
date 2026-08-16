package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

type fakeHelperOperations struct {
	events        []string
	installerArgs []string
	installerWait bool
	healthTimeout time.Duration
	stopErrors    []error
	backupErr     error
	installerErr  error
	startErrors   []error
	healthErrors  []error
	restoreErr    error
	restoreErrors []error
	applyErr      error
	applyErrors   []error
	cleanupErr    error
}

type fakeTrayHelperOperations struct {
	*fakeHelperOperations
	trayEvents        []string
	trayRunning       bool
	stopTrayErr       error
	startTrayErr      error
	startedExecutable string
}

type fakeHelperRuntime struct {
	validateErr error
	lockErr     error
	validated   bool
	locked      bool
	released    bool
}

type fakeExecutionLockObjectOperations struct {
	handle       uintptr
	createErr    error
	existingErr  error
	initialOwner bool
	closeCalls   int
}

func (operations *fakeExecutionLockObjectOperations) Create(initialOwner bool) (uintptr, error) {
	operations.initialOwner = initialOwner
	return operations.handle, operations.createErr
}

func (operations *fakeExecutionLockObjectOperations) Close(uintptr) error {
	operations.closeCalls++
	return nil
}

func (operations *fakeExecutionLockObjectOperations) IsAlreadyExists(err error) bool {
	return errors.Is(err, operations.existingErr)
}

func (f *fakeHelperRuntime) ValidateRunningUpdater(string) error {
	f.validated = true
	return f.validateErr
}

func (f *fakeHelperRuntime) AcquireExecutionLock() (func() error, error) {
	if f.lockErr != nil {
		return nil, f.lockErr
	}
	f.locked = true
	return func() error {
		f.released = true
		return nil
	}, nil
}

func TestExecutionObjectLockUsesHandleLifetimeInsteadOfThreadOwnership(t *testing.T) {
	operations := &fakeExecutionLockObjectOperations{handle: 42}

	release, err := acquireExecutionObjectLock(operations)
	if err != nil {
		t.Fatalf("acquireExecutionObjectLock() error = %v", err)
	}
	if operations.initialOwner {
		t.Fatal("execution lock acquired thread-affine object ownership")
	}
	if err := release(); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("second release() error = %v", err)
	}
	if operations.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", operations.closeCalls)
	}
}

func TestExecutionObjectLockRejectsExistingObjectAndClosesReturnedHandle(t *testing.T) {
	existingErr := errors.New("already exists")
	operations := &fakeExecutionLockObjectOperations{
		handle:      43,
		createErr:   existingErr,
		existingErr: existingErr,
	}

	release, err := acquireExecutionObjectLock(operations)

	if release != nil {
		t.Fatal("acquireExecutionObjectLock() returned a release function for an existing lock")
	}
	if !errors.Is(err, existingErr) {
		t.Fatalf("acquireExecutionObjectLock() error = %v", err)
	}
	if operations.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", operations.closeCalls)
	}
}

type fakeLaunchOperations struct {
	helperPath string
	copyFrom   string
	copyDir    string
	started    string
	args       []string
	detached   DetachedLaunchOptions
	copyErr    error
	startErr   error
	removed    string
}

func (f *fakeLaunchOperations) CopyTemporaryHelper(source, directory string) (string, error) {
	f.copyFrom = source
	f.copyDir = directory
	return f.helperPath, f.copyErr
}

func (f *fakeLaunchOperations) StartDetached(_ context.Context, executable string, args []string, options DetachedLaunchOptions) error {
	f.started = executable
	f.args = append([]string(nil), args...)
	f.detached = options
	return f.startErr
}

func (f *fakeLaunchOperations) Remove(path string) error {
	f.removed = path
	return nil
}

func (f *fakeHelperOperations) StopService(context.Context) error {
	f.events = append(f.events, "stop")
	return popError(&f.stopErrors)
}

func (f *fakeHelperOperations) BackupExecutable(_, _ string) error {
	f.events = append(f.events, "backup")
	return f.backupErr
}

func (f *fakeHelperOperations) RunInstaller(ctx context.Context, _ string, args []string) error {
	f.events = append(f.events, "install")
	f.installerArgs = append([]string(nil), args...)
	if f.installerWait {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.installerErr
}

func (f *fakeHelperOperations) StartService(context.Context) error {
	f.events = append(f.events, "start")
	return popError(&f.startErrors)
}

func (f *fakeHelperOperations) WaitHealthy(_ context.Context, timeout time.Duration) error {
	f.events = append(f.events, "health")
	f.healthTimeout = timeout
	return popError(&f.healthErrors)
}

func (f *fakeHelperOperations) RestoreExecutable(_, _ string) error {
	f.events = append(f.events, "restore")
	if len(f.restoreErrors) != 0 {
		return popError(&f.restoreErrors)
	}
	return f.restoreErr
}

func (f *fakeHelperOperations) ApplyServiceConfiguration(string) error {
	f.events = append(f.events, "apply")
	if len(f.applyErrors) != 0 {
		return popError(&f.applyErrors)
	}
	return f.applyErr
}

func (f *fakeHelperOperations) Cleanup(_ ...string) error {
	f.events = append(f.events, "cleanup")
	return f.cleanupErr
}

func (f *fakeTrayHelperOperations) StopTray(context.Context) (bool, error) {
	f.trayEvents = append(f.trayEvents, "stop")
	return f.trayRunning, f.stopTrayErr
}

func (f *fakeTrayHelperOperations) StartTray(_ context.Context, executable string) error {
	f.trayEvents = append(f.trayEvents, "start")
	f.startedExecutable = executable
	return f.startTrayErr
}

func TestHelperRunsSilentInstallerAndCleansSuccessfulUpdate(t *testing.T) {
	ops := &fakeHelperOperations{}
	options := testHelperOptions(ops)
	options.HealthTimeout = 5 * time.Minute

	err := RunHelper(context.Background(), options)

	if err != nil {
		t.Fatalf("RunHelper() error = %v", err)
	}
	if !slices.Equal(ops.events, []string{"stop", "backup", "install", "apply", "start", "health", "cleanup"}) {
		t.Fatalf("events = %v", ops.events)
	}
	wantArgs := []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/FORCECLOSEAPPLICATIONS", "/UPDATEHELPER"}
	if !slices.Equal(ops.installerArgs, wantArgs) {
		t.Fatalf("installer args = %v, want %v", ops.installerArgs, wantArgs)
	}
	if ops.healthTimeout != 60*time.Second {
		t.Fatalf("health timeout = %s, want 60s", ops.healthTimeout)
	}
}

func TestHelperStopsAndRestartsRunningTrayAroundSuccessfulUpdate(t *testing.T) {
	ops := &fakeTrayHelperOperations{
		fakeHelperOperations: &fakeHelperOperations{},
		trayRunning:          true,
	}
	options := testHelperOptions(ops)

	err := RunHelper(context.Background(), options)

	if err != nil {
		t.Fatalf("RunHelper() error = %v", err)
	}
	if !slices.Equal(ops.trayEvents, []string{"stop", "start"}) {
		t.Fatalf("tray events = %v, want stop then start", ops.trayEvents)
	}
	if ops.startedExecutable != options.ExecutablePath {
		t.Fatalf("started tray executable = %q, want %q", ops.startedExecutable, options.ExecutablePath)
	}
}

func TestHelperRestartsRunningTrayAfterRollback(t *testing.T) {
	installErr := errors.New("installer failed")
	ops := &fakeTrayHelperOperations{
		fakeHelperOperations: &fakeHelperOperations{installerErr: installErr},
		trayRunning:          true,
	}

	err := RunHelper(context.Background(), testHelperOptions(ops))

	if !errors.Is(err, installErr) {
		t.Fatalf("RunHelper() error = %v, want %v", err, installErr)
	}
	if !slices.Equal(ops.trayEvents, []string{"stop", "start"}) {
		t.Fatalf("tray events = %v, want stop then start", ops.trayEvents)
	}
}

func TestHelperAbortsUpdateWhenRunningTrayCannotStop(t *testing.T) {
	stopErr := errors.New("tray did not stop")
	ops := &fakeTrayHelperOperations{
		fakeHelperOperations: &fakeHelperOperations{},
		trayRunning:          true,
		stopTrayErr:          stopErr,
	}

	err := RunHelper(context.Background(), testHelperOptions(ops))

	if !errors.Is(err, stopErr) {
		t.Fatalf("RunHelper() error = %v, want %v", err, stopErr)
	}
	if !slices.Equal(ops.events, []string{"stop", "start"}) {
		t.Fatalf("service events = %v, want stopped then restarted", ops.events)
	}
}

func TestHelperRestoresLastKnownGoodForEveryPostBackupFailure(t *testing.T) {
	installErr := errors.New("installer failed")
	startErr := errors.New("new service failed")
	healthErr := errors.New("pipe unavailable")
	tests := []struct {
		name string
		ops  *fakeHelperOperations
		want []string
		err  error
	}{
		{name: "installer", ops: &fakeHelperOperations{installerErr: installErr}, want: []string{"stop", "backup", "install", "stop", "restore", "apply", "start", "health"}, err: installErr},
		{name: "start", ops: &fakeHelperOperations{startErrors: []error{startErr, nil}}, want: []string{"stop", "backup", "install", "apply", "start", "stop", "restore", "apply", "start", "health"}, err: startErr},
		{name: "health", ops: &fakeHelperOperations{healthErrors: []error{healthErr, nil}}, want: []string{"stop", "backup", "install", "apply", "start", "health", "stop", "restore", "apply", "start", "health"}, err: healthErr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := RunHelper(context.Background(), testHelperOptions(test.ops))

			if !errors.Is(err, test.err) {
				t.Fatalf("RunHelper() error = %v, want cause %v", err, test.err)
			}
			if !slices.Equal(test.ops.events, test.want) {
				t.Fatalf("events = %v, want %v", test.ops.events, test.want)
			}
		})
	}
}

func TestHelperTimesOutHungInstallerAndRestoresLastKnownGood(t *testing.T) {
	ops := &fakeHelperOperations{installerWait: true}
	options := testHelperOptions(ops)
	options.InstallerTimeout = 10 * time.Millisecond

	started := time.Now()
	err := RunHelper(context.Background(), options)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunHelper() error = %v, want installer deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("RunHelper() exceeded installer deadline: %s", elapsed)
	}
	wantEvents := []string{"stop", "backup", "install", "stop", "restore", "apply", "start", "health"}
	if !slices.Equal(ops.events, wantEvents) {
		t.Fatalf("events = %v, want %v", ops.events, wantEvents)
	}
}

func TestHelperDoesNotRestoreExecutableWhenRollbackStopFails(t *testing.T) {
	healthErr := errors.New("new service unhealthy")
	stopErr := errors.New("rollback stop failed")
	ops := &fakeHelperOperations{
		healthErrors: []error{healthErr},
		stopErrors:   []error{nil, stopErr},
	}

	err := RunHelper(context.Background(), testHelperOptions(ops))

	for _, want := range []error{healthErr, stopErr} {
		if !errors.Is(err, want) {
			t.Fatalf("RunHelper() error = %v, missing %v", err, want)
		}
	}
	if !slices.Equal(ops.events, []string{"stop", "backup", "install", "apply", "start", "health", "stop"}) {
		t.Fatalf("events = %v", ops.events)
	}
}

func TestHelperReappliesOldServiceConfigurationAndReportsRollbackHealthFailure(t *testing.T) {
	updateHealthErr := errors.New("new pipe unhealthy")
	rollbackHealthErr := errors.New("old pipe unhealthy")
	ops := &fakeHelperOperations{healthErrors: []error{updateHealthErr, rollbackHealthErr}}

	err := RunHelper(context.Background(), testHelperOptions(ops))

	for _, want := range []error{updateHealthErr, rollbackHealthErr} {
		if !errors.Is(err, want) {
			t.Fatalf("RunHelper() error = %v, missing %v", err, want)
		}
	}
	wantEvents := []string{"stop", "backup", "install", "apply", "start", "health", "stop", "restore", "apply", "start", "health"}
	if !slices.Equal(ops.events, wantEvents) {
		t.Fatalf("events = %v, want %v", ops.events, wantEvents)
	}
}

func TestHelperDoesNotStartRollbackServiceWhenConfigurationRestoreFails(t *testing.T) {
	updateHealthErr := errors.New("new pipe unhealthy")
	configurationErr := errors.New("restore service configuration failed")
	ops := &fakeHelperOperations{
		healthErrors: []error{updateHealthErr, nil},
		applyErrors:  []error{nil, configurationErr},
	}

	err := RunHelper(context.Background(), testHelperOptions(ops))

	for _, want := range []error{updateHealthErr, configurationErr} {
		if !errors.Is(err, want) {
			t.Fatalf("RunHelper() error = %v, missing %v", err, want)
		}
	}
	wantEvents := []string{"stop", "backup", "install", "apply", "start", "health", "stop", "restore", "apply"}
	if !slices.Equal(ops.events, wantEvents) {
		t.Fatalf("events = %v, want %v", ops.events, wantEvents)
	}
}

func TestHelperRestartsUnmodifiedServiceWhenBackupFails(t *testing.T) {
	backupErr := errors.New("backup denied")
	restartErr := errors.New("restart failed")
	ops := &fakeHelperOperations{backupErr: backupErr, startErrors: []error{restartErr}}

	err := RunHelper(context.Background(), testHelperOptions(ops))

	if !errors.Is(err, backupErr) || !errors.Is(err, restartErr) {
		t.Fatalf("RunHelper() error = %v", err)
	}
	if !slices.Equal(ops.events, []string{"stop", "backup", "start"}) {
		t.Fatalf("events = %v", ops.events)
	}
}

func TestHelperRestartsUnmodifiedServiceWhenInitialStopFails(t *testing.T) {
	stopErr := errors.New("service stop timed out")
	restartErr := errors.New("service restart failed")
	ops := &fakeHelperOperations{
		stopErrors:  []error{stopErr},
		startErrors: []error{restartErr},
	}

	err := RunHelper(context.Background(), testHelperOptions(ops))

	if !errors.Is(err, stopErr) || !errors.Is(err, restartErr) {
		t.Fatalf("RunHelper() error = %v", err)
	}
	if !slices.Equal(ops.events, []string{"stop", "start"}) {
		t.Fatalf("events = %v", ops.events)
	}
}

func TestHelperValidatesOptionsBeforeStoppingService(t *testing.T) {
	ops := &fakeHelperOperations{}
	options := testHelperOptions(ops)
	options.InstallerPath = ""

	if err := RunHelper(context.Background(), options); err == nil {
		t.Fatal("RunHelper() accepted incomplete options")
	}
	if len(ops.events) != 0 {
		t.Fatalf("events = %v", ops.events)
	}
}

func TestLaunchHelperUsesTemporaryCopyAndCredentialFreeArgumentVector(t *testing.T) {
	operations := &fakeLaunchOperations{helperPath: "/Program Data/Ace/update helper.exe"}
	options := LaunchOptions{
		ExecutablePath: "/Program Files/Ace/AceAgent.exe",
		InstallerPath:  "/Program Data/Ace/setup 0.2.0.exe",
		BackupPath:     "/Program Data/Ace/AceAgent lkg.exe",
		StagingDir:     "/Program Data/Ace",
		Version:        "0.2.0",
		Operations:     operations,
	}

	if err := LaunchHelper(context.Background(), options); err != nil {
		t.Fatalf("LaunchHelper() error = %v", err)
	}
	if operations.copyFrom != options.ExecutablePath || operations.copyDir != options.StagingDir || operations.started != operations.helperPath {
		t.Fatalf("launch operations = %#v", operations)
	}
	want := []string{
		"update-helper",
		"--installer", options.InstallerPath,
		"--executable", options.ExecutablePath,
		"--backup", options.BackupPath,
		"--version", options.Version,
	}
	if !slices.Equal(operations.args, want) {
		t.Fatalf("helper args = %#v, want %#v", operations.args, want)
	}
	if !operations.detached.BreakawayFromJob {
		t.Fatal("helper launch did not request Windows Job breakaway")
	}
}

func TestLaunchHelperRemovesTemporaryCopyWhenDetachedStartFails(t *testing.T) {
	startErr := errors.New("CreateProcess failed")
	operations := &fakeLaunchOperations{helperPath: "/staging/update-helper.exe", startErr: startErr}
	options := LaunchOptions{
		ExecutablePath: "/program/AceAgent.exe",
		InstallerPath:  "/staging/setup.exe",
		BackupPath:     "/staging/lkg.exe",
		StagingDir:     "/staging",
		Version:        "0.2.0",
		Operations:     operations,
	}

	err := LaunchHelper(context.Background(), options)

	if !errors.Is(err, startErr) {
		t.Fatalf("LaunchHelper() error = %v", err)
	}
	if operations.removed != operations.helperPath {
		t.Fatalf("removed = %q, want %q", operations.removed, operations.helperPath)
	}
}

func TestWaitForHealthyEnforcesOverallDeadlineOnBlockingAttempt(t *testing.T) {
	started := time.Now()
	err := waitForHealthy(context.Background(), 10*time.Millisecond, time.Millisecond, func(ctx context.Context) (bool, error) {
		<-ctx.Done()
		return false, ctx.Err()
	})

	if err == nil {
		t.Fatal("waitForHealthy() returned no timeout error")
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("waitForHealthy() exceeded hard deadline: %s", elapsed)
	}
}

func TestHelperFailsClosedBeforeMutationForContextPlatformAndStopErrors(t *testing.T) {
	if err := RunHelper(nil, testHelperOptions(&fakeHelperOperations{})); err == nil {
		t.Fatal("RunHelper() accepted nil context")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunHelper(canceled, testHelperOptions(&fakeHelperOperations{})); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunHelper() canceled error = %v", err)
	}
	stopErr := errors.New("SCM unavailable")
	ops := &fakeHelperOperations{stopErrors: []error{stopErr}}
	if err := RunHelper(context.Background(), testHelperOptions(ops)); !errors.Is(err, stopErr) {
		t.Fatalf("RunHelper() stop error = %v", err)
	}
	options := testHelperOptions(nil)
	if err := RunHelper(context.Background(), options); err == nil {
		t.Fatal("RunHelper() used unsupported non-Windows operations")
	}
}

func TestHelperReportsCleanupWarningWithoutFailingHealthyAgent(t *testing.T) {
	cleanupErr := errors.New("cleanup denied")
	ops := &fakeHelperOperations{cleanupErr: cleanupErr}
	options := testHelperOptions(ops)
	var warning error
	options.CleanupWarning = func(err error) { warning = err }

	err := RunHelper(context.Background(), options)

	if err != nil {
		t.Fatalf("RunHelper() error = %v", err)
	}
	if !errors.Is(warning, cleanupErr) {
		t.Fatalf("cleanup warning = %v", warning)
	}
	if !slices.Equal(ops.events, []string{"stop", "backup", "install", "apply", "start", "health", "cleanup"}) {
		t.Fatalf("events = %v", ops.events)
	}
}

func TestHelperRollsBackWhenUpdatedServiceConfigurationFails(t *testing.T) {
	configurationErr := errors.New("updated service configuration failed")
	ops := &fakeHelperOperations{applyErrors: []error{configurationErr, nil}}

	err := RunHelper(context.Background(), testHelperOptions(ops))

	if !errors.Is(err, configurationErr) {
		t.Fatalf("RunHelper() error = %v, want cause %v", err, configurationErr)
	}
	wantEvents := []string{"stop", "backup", "install", "apply", "stop", "restore", "apply", "start", "health"}
	if !slices.Equal(ops.events, wantEvents) {
		t.Fatalf("events = %v, want %v", ops.events, wantEvents)
	}
}

func TestHelperRetriesTransientLastKnownGoodRestoreFailure(t *testing.T) {
	updateErr := errors.New("installer failed")
	sharingViolation := errors.New("sharing violation")
	ops := &fakeHelperOperations{
		installerErr:  updateErr,
		restoreErrors: []error{sharingViolation, nil},
	}

	err := RunHelper(context.Background(), testHelperOptions(ops))

	if !errors.Is(err, updateErr) {
		t.Fatalf("RunHelper() error = %v, want cause %v", err, updateErr)
	}
	wantEvents := []string{"stop", "backup", "install", "stop", "restore", "restore", "apply", "start", "health"}
	if !slices.Equal(ops.events, wantEvents) {
		t.Fatalf("events = %v, want %v", ops.events, wantEvents)
	}
}

func TestHelperAttemptsServiceRecoveryWhenLastKnownGoodRestoreFails(t *testing.T) {
	updateErr := errors.New("installer failed")
	restoreErr := errors.New("executable remains locked")
	ops := &fakeHelperOperations{installerErr: updateErr, restoreErr: restoreErr}

	err := RunHelper(context.Background(), testHelperOptions(ops))

	for _, want := range []error{updateErr, restoreErr} {
		if !errors.Is(err, want) {
			t.Fatalf("RunHelper() error = %v, missing %v", err, want)
		}
	}
	if len(ops.events) < 7 {
		t.Fatalf("events = %v, service recovery was not attempted", ops.events)
	}
	wantTail := []string{"apply", "start", "health"}
	if !slices.Equal(ops.events[len(ops.events)-len(wantTail):], wantTail) {
		t.Fatalf("events = %v, want recovery tail %v", ops.events, wantTail)
	}
}

func TestHelperRecordsCleanupMarkerWhenWarningCallbackIsPresent(t *testing.T) {
	cleanupErr := errors.New("cleanup denied")
	options := testHelperOptions(&fakeHelperOperations{cleanupErr: cleanupErr})
	callbackCalled := false
	markerCalled := false
	options.CleanupWarning = func(error) { callbackCalled = true }
	options.cleanupMarker = func(string, error) { markerCalled = true }

	if err := RunHelper(context.Background(), options); err != nil {
		t.Fatalf("RunHelper() error = %v", err)
	}
	if !callbackCalled || !markerCalled {
		t.Fatalf("callback=%t marker=%t", callbackCalled, markerCalled)
	}
}

func TestUpdaterIdentityRejectsAgentHardlinkPendingAndOutsidePath(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "installed", "AceAgent.exe")
	if err := os.MkdirAll(filepath.Dir(installed), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(filepath.Dir(installed), "AceAgentUpdater.exe")
	if err := os.Link(installed, expected); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(filepath.Dir(installed), "AceAgentUpdater.next.exe")
	if err := os.WriteFile(pending, []byte("pending"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.exe")
	if err := os.WriteFile(outside, []byte("updater"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, running := range []string{installed, expected, pending, outside} {
		operations := filesystemIdentityOperations{running: running}
		if err := validateUpdaterIdentity(installed, operations); err == nil {
			t.Fatalf("running updater %q was accepted", running)
		}
	}
}

func TestUpdaterIdentityAcceptsOnlyFixedSiblingExecutable(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "AceAgent.exe")
	if err := os.WriteFile(installed, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	updater := filepath.Join(root, "AceAgentUpdater.exe")
	if err := os.WriteFile(updater, []byte("updater"), 0o700); err != nil {
		t.Fatal(err)
	}

	operations := filesystemIdentityOperations{running: updater}
	if err := validateUpdaterIdentity(installed, operations); err != nil {
		t.Fatalf("validateUpdaterIdentity() error = %v", err)
	}
}

func TestHelperIdentityAndCrossProcessLockRunBeforeServiceMutation(t *testing.T) {
	identityErr := errors.New("running executable is installed Agent")
	runtime := &fakeHelperRuntime{validateErr: identityErr}
	ops := &fakeHelperOperations{}
	options := testHelperOptions(ops)
	options.Runtime = runtime

	if err := RunHelper(context.Background(), options); !errors.Is(err, identityErr) {
		t.Fatalf("RunHelper() error = %v", err)
	}
	if len(ops.events) != 0 || runtime.locked {
		t.Fatalf("operations=%v runtime=%#v", ops.events, runtime)
	}

	lockErr := errors.New("another helper is active")
	runtime = &fakeHelperRuntime{lockErr: lockErr}
	options.Runtime = runtime
	if err := RunHelper(context.Background(), options); !errors.Is(err, lockErr) {
		t.Fatalf("RunHelper() lock error = %v", err)
	}
	if len(ops.events) != 0 {
		t.Fatalf("service mutated before helper lock: %v", ops.events)
	}
}

func TestLaunchHelperFailsClosedForInvalidContextPathsAndPlatform(t *testing.T) {
	valid := LaunchOptions{
		ExecutablePath: "/program/AceAgent.exe",
		InstallerPath:  "/staging/setup.exe",
		BackupPath:     "/staging/lkg.exe",
		StagingDir:     "/staging",
		Version:        "0.2.0",
	}
	if err := LaunchHelper(nil, valid); err == nil {
		t.Fatal("LaunchHelper() accepted nil context")
	}
	missing := valid
	missing.Version = ""
	if err := LaunchHelper(context.Background(), missing); err == nil {
		t.Fatal("LaunchHelper() accepted missing version")
	}
	relative := valid
	relative.BackupPath = "lkg.exe"
	if err := LaunchHelper(context.Background(), relative); err == nil {
		t.Fatal("LaunchHelper() accepted relative path")
	}
	if err := LaunchHelper(context.Background(), valid); err == nil {
		t.Fatal("LaunchHelper() used unsupported non-Windows operations")
	}
}

func TestLaunchHelperPropagatesTemporaryCopyFailureWithoutStarting(t *testing.T) {
	copyErr := errors.New("copy denied")
	operations := &fakeLaunchOperations{copyErr: copyErr}
	options := LaunchOptions{
		ExecutablePath: "/program/AceAgent.exe",
		InstallerPath:  "/staging/setup.exe",
		BackupPath:     "/staging/lkg.exe",
		StagingDir:     "/staging",
		Version:        "0.2.0",
		Operations:     operations,
	}

	if err := LaunchHelper(context.Background(), options); !errors.Is(err, copyErr) {
		t.Fatalf("LaunchHelper() copy error = %v", err)
	}
	if operations.started != "" {
		t.Fatalf("started helper = %q", operations.started)
	}
}

func TestNonWindowsVersionProbeFailsExplicitly(t *testing.T) {
	if version, err := CurrentOSVersion(); err == nil || version != "" {
		t.Fatalf("CurrentOSVersion() = %q, %v", version, err)
	}
}

func testHelperOptions(operations HelperOperations) HelperOptions {
	return HelperOptions{
		InstallerPath:   "/staging/AceAgentSetup-windows-amd64-V0.2.0.exe",
		ExecutablePath:  "/program/AceAgent.exe",
		BackupPath:      "/staging/AceAgent-0.1.0.lkg.exe",
		Version:         "0.2.0",
		HealthTimeout:   10 * time.Second,
		Operations:      operations,
		StagingDir:      "/staging",
		Runtime:         &fakeHelperRuntime{},
		restoreTimeout:  20 * time.Millisecond,
		restoreInterval: time.Millisecond,
	}
}

type filesystemIdentityOperations struct {
	running string
}

func (operations filesystemIdentityOperations) RunningExecutable() (string, error) {
	return operations.running, nil
}

func (filesystemIdentityOperations) FinalPath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

func (filesystemIdentityOperations) SameFile(left, right string) (bool, error) {
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false, err
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		return false, err
	}
	return os.SameFile(leftInfo, rightInfo), nil
}

func popError(values *[]error) error {
	if len(*values) == 0 {
		return nil
	}
	value := (*values)[0]
	*values = (*values)[1:]
	return value
}
