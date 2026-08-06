# Ace Agent Windows Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver an installable Windows 10/11 x64 Ace Agent with a LocalSystem Service, native tray enrollment, rotating logs and diagnostics, signed silent updates, Inno Setup packaging, and a disposable DSM release builder.

**Architecture:** Install one `AceAgent.exe` that runs as separate Service, tray, diagnostic, version, Service-management, and update-helper processes selected by command mode. Keep the heartbeat worker platform-neutral, use a bounded ACL-protected Windows Named Pipe for tray-to-Service requests, publish Ed25519-signed release manifests from a disposable Docker/Wine builder, and serve persistent release artifacts directly from DSM Nginx.

**Tech Stack:** Go 1.26, `golang.org/x/sys/windows/svc`, `github.com/Microsoft/go-winio`, `github.com/lxn/walk`, `gopkg.in/natefinch/lumberjack.v2`, `golang.org/x/mod/semver`, Inno Setup 6, Wine, Docker Compose, Vue 3, Vitest, Nginx.

## Global Constraints

- Support Windows 10/11 x64 only; do not add Windows 7 or 32-bit builds.
- Preserve the existing Linux CLI agent and all existing enrollment/heartbeat API contracts.
- Install exactly one runtime binary at `C:\Program Files\Ace IT Center\AceAgent.exe`.
- Run `AceAgent.exe service` as the LocalSystem `AceITCenterAgent` Windows Service with automatic delayed start.
- Run `AceAgent.exe tray` once per interactive user session; exiting the tray must not stop the Service.
- Running the Windows binary without arguments must open the tray/settings experience without a console flash.
- Enrollment occurs after installation; the tray collects an editable server URL defaulting to `http://it.ace-station.top:1111` and a one-time token.
- Never write enrollment tokens or agent credentials to logs, command-line arguments, diagnostics, update metadata, or tray responses.
- Store runtime state under `C:\ProgramData\AceITCenter\` with System/Administrators-only credential access.
- Use `\\.\pipe\AceITCenterAgent`, bounded 64 KiB JSON messages, explicit methods, and per-request timeouts.
- Rotate JSONL logs at 10 MiB, retain seven files, and expire after 14 days.
- Check stable updates at startup and every hour with jitter; install valid newer releases silently.
- Verify every update with an embedded Ed25519 public key, exact size, and SHA-256; reject downgrade and every cross-origin redirect.
- Generate or locate the persistent DSM signing key before building a release; derive its public key inside the disposable builder and inject that key into the Agent binary without exposing the private key.
- Keep a last-known-good binary and roll back when post-update Service/pipe health validation fails.
- Use Inno Setup; the first release is unsigned by Authenticode and may show SmartScreen.
- Build with a disposable DSM Docker/Wine container; persist artifacts and signing key outside the image/container.
- Publish Windows releases without rebuilding the primary Web image after the release mount is configured.
- Use TDD for behavior changes, keep test output warning-free, and commit only files owned by each task.

---

## File Map

### Shared Agent Core

- Modify `agent/cmd/ace-agent/main.go`: thin cross-platform entrypoint and version injection.
- Create `agent/internal/buildinfo/buildinfo.go`: build version, commit, and date values.
- Create `agent/internal/app/commands.go`: mode parsing independent of Windows APIs.
- Create `agent/internal/app/worker.go`: cancellable heartbeat worker and status snapshots.
- Modify `agent/internal/agent/config.go`: Windows-safe atomic config and sanitized config projection.

### Windows Runtime

- Create `agent/internal/windowsservice/*_windows.go`: Service handler, SCM install/remove, recovery configuration, and console attachment.
- Create `agent/internal/controller/controller.go`: enrollment, worker state, restart, diagnostics, and update orchestration.
- Create `agent/internal/ipc/protocol.go`: bounded request/response model and method allowlist.
- Create `agent/internal/ipc/pipe_windows.go`: `go-winio` Named Pipe listener/client with SDDL.
- Create `agent/internal/tray/presenter.go`: testable tray state/actions.
- Create `agent/internal/tray/native_windows.go`: Walk tray icon and enrollment/status dialog.

### Operations and Release

- Create `agent/internal/logging/logging.go`: rotating JSONL handler and redaction.
- Create `agent/internal/diagnostics/bundle.go`: sanitized ZIP diagnostics.
- Create `agent/internal/update/manifest.go`: canonical manifest signing/verification.
- Create `agent/internal/update/checker.go`: update polling, download, verification, and staging.
- Create `agent/internal/update/helper_windows.go`: replacement, Service health check, and rollback.
- Create `tools/cmd/ace-release/main.go`: key generation, release manifest signing, and verification.
- Create `installer/windows/AceAgent.iss`: install, upgrade, uninstall, purge, Service, and tray startup.
- Create `installer/windows/assets/*`: application icon and installer assets.
- Create `deploy/windows-builder.Dockerfile`: pinned Go/Wine/Inno release builder.
- Create `deploy/windows-builder.compose.yaml`: one-shot builder and read-only signing key mounts.
- Create `scripts/publish-windows-release.sh`: atomic release publication and stable alias.
- Modify `deploy/compose.yaml`, `deploy/nginx.conf`, `deploy/.env.example`, `deploy/README.md`: release bind mount and operations.

### Web Integration

- Modify `frontend/src/components/AgentDownloads.vue`: installer download and post-install tray instructions.
- Modify `frontend/src/components/AgentDownloads.test.ts`: exact installer route/copy coverage.
- Modify `frontend/src/components/OperationsWorkspace.vue`: Windows enrollment no longer shows a raw CLI command; Linux keeps an elevated command.
- Modify `frontend/src/components/OperationsWorkspace.test.ts`: updated Windows/Linux workflow coverage.

---

### Task 1: Extract a Testable Worker and Command Dispatcher

**Files:**
- Create: `agent/internal/buildinfo/buildinfo.go`
- Create: `agent/internal/app/commands.go`
- Create: `agent/internal/app/commands_test.go`
- Create: `agent/internal/app/worker.go`
- Create: `agent/internal/app/worker_test.go`
- Modify: `agent/cmd/ace-agent/main.go`

**Interfaces:**
- Produces: `app.ParseMode(goos string, args []string) (app.Mode, []string, error)`.
- Produces: `app.Worker.Run(ctx context.Context, config agent.Config, interval time.Duration) error`.
- Produces: `app.StatusSnapshot` with state, node ID, server URL, version, last heartbeat, and sanitized error.
- Preserves: current Linux flags and foreground behavior.

- [ ] **Step 1: Add failing command-dispatch tests**

```go
func TestParseModeDefaults(t *testing.T) {
    windows, _, err := ParseMode("windows", nil)
    if err != nil || windows != ModeTray { t.Fatalf("windows mode = %q, err=%v", windows, err) }
    linux, args, err := ParseMode("linux", []string{"-once"})
    if err != nil || linux != ModeForeground || !slices.Equal(args, []string{"-once"}) {
        t.Fatalf("linux mode=%q args=%v err=%v", linux, args, err)
    }
}

func TestParseModeRejectsUnknownWindowsMode(t *testing.T) {
    _, _, err := ParseMode("windows", []string{"unknown"})
    if err == nil { t.Fatal("expected unknown mode error") }
}
```

- [ ] **Step 2: Run the dispatcher tests and verify RED**

Run: `go test ./agent/internal/app -run 'TestParseMode' -count=1`

Expected: FAIL because `ParseMode` and modes do not exist.

- [ ] **Step 3: Implement exact modes and build metadata**

```go
type Mode string
const (
    ModeForeground   Mode = "foreground"
    ModeService      Mode = "service"
    ModeTray         Mode = "tray"
    ModeDiagnose     Mode = "diagnose"
    ModeVersion      Mode = "version"
    ModeUpdateHelper Mode = "update-helper"
)

func ParseMode(goos string, args []string) (Mode, []string, error) {
    if goos != "windows" { return ModeForeground, args, nil }
    if len(args) == 0 { return ModeTray, nil, nil }
    switch args[0] {
    case "service", "tray", "diagnose", "version", "update-helper":
        return Mode(args[0]), args[1:], nil
    default:
        return "", nil, fmt.Errorf("unknown Ace Agent mode %q", args[0])
    }
}
```

Use variables, not constants, so the release build can inject exact metadata:

```go
package buildinfo
var Version = "0.2.0-dev"
var Commit = "unknown"
var BuiltAt = "unknown"
var UpdatePublicKey = ""
```

- [ ] **Step 4: Add failing worker lifecycle tests**

Use fake collector/client and a 1 ms interval to assert immediate heartbeat, periodic heartbeat, state publication, retry without exit, and context cancellation.

```go
func TestWorkerKeepsRunningAfterHeartbeatFailure(t *testing.T) {
    client := &fakeHeartbeatClient{errors: []error{errors.New("offline"), nil}}
    worker := NewWorker(Dependencies{Client: client, Collect: fakeCollect, Version: "0.2.0"})
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()
    if err := worker.Run(ctx, validConfig(), time.Millisecond); err != nil { t.Fatal(err) }
    if client.calls < 2 { t.Fatalf("heartbeat calls=%d", client.calls) }
}
```

- [ ] **Step 5: Implement the worker and make foreground mode use it**

Keep enrollment/config parsing in the foreground adapter for now. `Worker.Run` must not call `os.Exit`; return typed errors and publish snapshots through `StatusSink`.

- [ ] **Step 6: Run focused and full Go tests**

Run:

```bash
go test ./agent/internal/app ./agent/internal/agent -count=1
go test ./... -count=1
```

Expected: all tests PASS and existing Linux agent behavior remains covered.

- [ ] **Step 7: Commit Task 1**

```bash
git add agent/cmd/ace-agent/main.go agent/internal/buildinfo agent/internal/app
git commit -m "refactor: 拆分 Agent 命令与心跳运行时"
```

---

### Task 2: Add Secure Configuration, Rotating Logs, and Diagnostics

**Files:**
- Modify: `agent/internal/agent/config.go`
- Modify: `agent/internal/agent/config_test.go`
- Create: `agent/internal/logging/logging.go`
- Create: `agent/internal/logging/logging_test.go`
- Create: `agent/internal/diagnostics/bundle.go`
- Create: `agent/internal/diagnostics/bundle_test.go`

**Interfaces:**
- Produces: `agent.SanitizedConfig` without credential.
- Produces: `logging.New(logging.Options) (*slog.Logger, io.Closer, error)`.
- Produces: `diagnostics.Create(ctx, Options) (string, error)`.
- Consumes: build metadata and status snapshot from Task 1.

- [ ] **Step 1: Write failing secret-redaction and diagnostic tests**

```go
func TestRedactingHandlerRemovesSecrets(t *testing.T) {
    var output bytes.Buffer
    logger := slog.New(NewRedactingHandler(slog.NewJSONHandler(&output, nil)))
    logger.Info("enroll", "token", "one-time", "credential", "device-secret", "server", "https://it.example")
    got := output.String()
    if strings.Contains(got, "one-time") || strings.Contains(got, "device-secret") { t.Fatal(got) }
    if !strings.Contains(got, "[REDACTED]") { t.Fatal(got) }
}

func TestDiagnosticBundleExcludesCredential(t *testing.T) {
    path, err := Create(context.Background(), testOptions("device-secret", "one-time"))
    if err != nil { t.Fatal(err) }
    content := readZipText(t, path)
    if strings.Contains(content, "device-secret") || strings.Contains(content, "one-time") { t.Fatal(content) }
}
```

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./agent/internal/logging ./agent/internal/diagnostics -count=1`

Expected: FAIL because both packages are missing.

- [ ] **Step 3: Implement logging with exact rotation settings**

Configure lumberjack with `MaxSize: 10`, `MaxBackups: 7`, `MaxAge: 14`, `Compress: true`. Redact keys case-insensitively when equal to `token`, `enrollment_token`, `credential`, `authorization`, or `password`.

- [ ] **Step 4: Add sanitized config and atomic-write tests**

Test Windows rename fallback through an injected file operation: close old handle, preserve mode/ACL setup hook, rename temporary file, and leave the previous file intact when replacement fails.

- [ ] **Step 5: Implement diagnostic ZIP contents**

Write `build.json`, `config.json`, `status.json`, `system.json`, and a bounded `logs/agent.log` copy. Never auto-upload the result.

- [ ] **Step 6: Run focused and full Go tests**

Run:

```bash
go test ./agent/internal/agent ./agent/internal/logging ./agent/internal/diagnostics -count=1
go test ./... -count=1
```

- [ ] **Step 7: Commit Task 2**

```bash
git add go.mod go.sum agent/internal/agent/config.go agent/internal/agent/config_test.go agent/internal/logging agent/internal/diagnostics
git commit -m "feat: 添加 Agent 安全日志与诊断包"
```

---

### Task 3: Implement the Controller and Bounded IPC Protocol

**Files:**
- Create: `agent/internal/controller/controller.go`
- Create: `agent/internal/controller/controller_test.go`
- Create: `agent/internal/ipc/protocol.go`
- Create: `agent/internal/ipc/protocol_test.go`
- Create: `agent/internal/ipc/transport.go`

**Interfaces:**
- Produces: `controller.Controller.Status() controller.Status`.
- Produces: `Controller.Enroll(ctx, serverURL, token string) error`, `RestartWorker`, `CheckUpdate`, and `CreateDiagnostics`.
- Produces IPC methods `status.get`, `enrollment.submit`, `worker.restart`, `update.check`, and `diagnostics.create`.
- Defers the Windows pipe transport to Task 4.

- [ ] **Step 1: Add failing controller enrollment tests**

Cover URL validation, token bounds (1..4096 bytes), config-directory preflight before network enrollment, successful atomic save, token non-retention, and no worker start after save failure.

```go
func TestEnrollPreflightsConfigBeforeConsumingToken(t *testing.T) {
    enroll := &fakeEnroller{}
    c := New(Dependencies{PreflightConfig: func() error { return fs.ErrPermission }, Enroller: enroll})
    err := c.Enroll(context.Background(), "https://it.example", "one-time")
    if !errors.Is(err, fs.ErrPermission) { t.Fatalf("err=%v", err) }
    if enroll.calls != 0 { t.Fatalf("enroll calls=%d", enroll.calls) }
}
```

- [ ] **Step 2: Run controller tests and verify RED**

Run: `go test ./agent/internal/controller -count=1`

- [ ] **Step 3: Implement a serialized controller state machine**

Use a mutex for state snapshots and a separate cancellable worker context. Do not hold the mutex during network, file, diagnostic, or update operations. Return typed errors with sanitized user messages.

- [ ] **Step 4: Add failing IPC codec tests**

```go
func TestDecodeRejectsOversizedMessage(t *testing.T) {
    _, err := Decode(io.LimitReader(strings.NewReader(strings.Repeat("x", MaxMessageBytes+1)), MaxMessageBytes+1))
    if !errors.Is(err, ErrMessageTooLarge) { t.Fatalf("err=%v", err) }
}

func TestRouterRejectsUnknownMethod(t *testing.T) {
    response := NewRouter(fakeController()).Handle(context.Background(), Request{ID: "1", Method: "credential.get"})
    if response.Error == nil || response.Error.Code != "method_not_allowed" { t.Fatalf("response=%#v", response) }
}
```

- [ ] **Step 5: Implement exact request/response envelopes**

```go
type Request struct {
    ID string `json:"id"`
    Method string `json:"method"`
    Params json.RawMessage `json:"params,omitempty"`
}
type Response struct {
    ID string `json:"id"`
    Result any `json:"result,omitempty"`
    Error *ResponseError `json:"error,omitempty"`
}
const MaxMessageBytes = 64 << 10
```

Responses expose node ID and server URL but never credential or token.

- [ ] **Step 6: Run focused and full tests**

Run:

```bash
go test ./agent/internal/controller ./agent/internal/ipc -count=1
go test ./... -count=1
```

- [ ] **Step 7: Commit Task 3**

```bash
git add agent/internal/controller agent/internal/ipc
git commit -m "feat: 添加 Agent 控制器与本地 IPC 协议"
```

---

### Task 4: Host the Controller as a Windows Service and Named Pipe

**Files:**
- Create: `agent/internal/windowsservice/service_windows.go`
- Create: `agent/internal/windowsservice/service_nonwindows.go`
- Create: `agent/internal/windowsservice/manager_windows.go`
- Create: `agent/internal/windowsservice/manager_nonwindows.go`
- Create: `agent/internal/windowsservice/manager_test.go`
- Create: `agent/internal/ipc/pipe_windows.go`
- Create: `agent/internal/ipc/pipe_nonwindows.go`
- Modify: `agent/cmd/ace-agent/main.go`

**Interfaces:**
- Produces: `windowsservice.Run(ctx, controller) error`.
- Produces: `windowsservice.Install(executable string) error` and `Uninstall() error`.
- Produces: `ipc.ListenWindows(ctx, router) error` and `ipc.DialWindows(ctx) (Client, error)`.

- [ ] **Step 1: Add failing Service command tests through injected SCM**

Test exact Service name `AceITCenterAgent`, display name `Ace IT Center Agent`, binary arguments `service`, start type automatic, delayed auto-start, and three restart recovery actions.

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./agent/internal/windowsservice -count=1`

- [ ] **Step 3: Implement Windows Service execution**

Use `svc.Run(serviceName, handler)`. Accept Stop and Shutdown controls, publish StartPending/Running/StopPending/Stopped, cancel the controller context, and wait for clean shutdown.

- [ ] **Step 4: Implement SCM install/uninstall and recovery**

Use `mgr.Connect`, `CreateService`, delayed auto-start config, and recovery actions. `Uninstall` stops an existing Service with timeout before delete and is idempotent.

- [ ] **Step 5: Implement the Named Pipe transport**

Use `winio.ListenPipe` with:

```text
D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)
```

Apply 10-second read/write deadlines and one bounded request per connection. Reject clients when the Service is stopping.

- [ ] **Step 6: Add Windows compile gates**

Run:

```bash
GOOS=windows GOARCH=amd64 go test -c ./agent/internal/windowsservice -o /tmp/windowsservice.test.exe
GOOS=windows GOARCH=amd64 go test -c ./agent/internal/ipc -o /tmp/ipc.test.exe
GOOS=windows GOARCH=amd64 go build -o /tmp/AceAgent.exe ./agent/cmd/ace-agent
```

Expected: all commands exit 0.

- [ ] **Step 7: Run full Go tests and commit**

```bash
go test ./... -count=1
git add go.mod go.sum agent/cmd/ace-agent/main.go agent/internal/windowsservice agent/internal/ipc
git commit -m "feat: 支持 Windows Service 与命名管道"
```

---

### Task 5: Build the Native Tray Enrollment Experience

**Files:**
- Create: `agent/internal/tray/presenter.go`
- Create: `agent/internal/tray/presenter_test.go`
- Create: `agent/internal/tray/native_windows.go`
- Create: `agent/internal/tray/native_nonwindows.go`
- Create: `agent/internal/tray/singleinstance_windows.go`
- Create: `agent/internal/tray/assets_windows.syso`
- Modify: `agent/cmd/ace-agent/main.go`

**Interfaces:**
- Produces: `tray.Presenter` mapping controller status to Chinese labels, icons, enabled actions, and notifications.
- Produces: `tray.Run(ctx, ipc.Client, tray.Options) error`.
- Consumes: IPC methods from Tasks 3-4.

- [ ] **Step 1: Add failing presenter tests**

```go
func TestWaitingStatusShowsEnrollmentForm(t *testing.T) {
    view := NewPresenter().View(controller.Status{State: controller.StateWaiting})
    if !view.ShowEnrollment || view.StatusText != "等待接入" { t.Fatalf("view=%#v", view) }
}

func TestOnlineStatusDoesNotExposeCredential(t *testing.T) {
    view := NewPresenter().View(controller.Status{State: controller.StateOnline, ServerURL: "https://it.example", NodeID: "node-1"})
    encoded, _ := json.Marshal(view)
    if bytes.Contains(encoded, []byte("credential")) { t.Fatal(string(encoded)) }
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./agent/internal/tray -count=1`

- [ ] **Step 3: Implement presenter states and actions**

Use gray/yellow/green/red/blue state icons for waiting/connecting/online/degraded/updating. Expose actions: open platform, configure enrollment, open logs, create diagnostics, check update, restart worker, and exit tray.

- [ ] **Step 4: Implement the Walk UI**

Create a NotifyIcon and native settings dialog. Default server input to `http://it.ace-station.top:1111`; keep it editable. Validate locally, disable submit while pending, and display sanitized Service errors. Clear the token control after every response.

- [ ] **Step 5: Implement per-user single instance and no-argument tray mode**

Use a session-scoped named mutex. A second launch signals/activates the existing tray and exits. Build the PE as GUI subsystem; attach a parent console only for explicit CLI modes.

- [ ] **Step 6: Run tests and Windows compile**

Run:

```bash
go test ./agent/internal/tray -count=1
GOOS=windows GOARCH=amd64 go build -ldflags="-H windowsgui" -o /tmp/AceAgent.exe ./agent/cmd/ace-agent
go test ./... -count=1
```

- [ ] **Step 7: Commit Task 5**

```bash
git add go.mod go.sum agent/cmd/ace-agent/main.go agent/internal/tray
git commit -m "feat: 添加 Windows 托盘接入体验"
```

---

### Task 6: Create the Inno Setup Installer and Service Lifecycle

**Files:**
- Create: `installer/windows/AceAgent.iss`
- Create: `installer/windows/assets/ace-agent.ico`
- Create: `installer/windows/assets/wizard-small.bmp`
- Create: `installer/windows/assets/wizard-large.bmp`
- Create: `scripts/build-windows-agent.sh`
- Create: `scripts/build-windows-agent.test.sh`
- Modify: `.gitignore`

**Interfaces:**
- Produces: `AceAgentSetup-<version>-windows-amd64.exe`.
- Consumes: GUI-subsystem `AceAgent.exe` from Task 5.
- Installs: `AceITCenterAgent` Service and HKLM tray startup.

- [ ] **Step 1: Add failing build-script interface tests**

Execute the build script against controlled temporary inputs. Assert clear failures for a missing version, malformed build time, missing Agent source, missing update public key, and unavailable ISCC in full-package mode. Assert `--agent-only` writes a GUI-subsystem PE without requiring ISCC. Do not test the installer by grepping its source text; Task 9 must compile and inspect the real installer artifact.

- [ ] **Step 2: Run the contract test and verify RED**

Run: `scripts/build-windows-agent.test.sh`

- [ ] **Step 3: Write the Inno Setup script**

Use constants injected by `/DAppVersion` and `/DSourceExe`. Include:

```ini
[Setup]
AppId={{6D4E847C-51D9-4BEA-BD3B-ACE17C3A1001}
AppName=Ace IT Center Agent
DefaultDirName={autopf}\Ace IT Center
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=admin
UninstallDisplayIcon={app}\AceAgent.exe

[Run]
Filename: "{app}\AceAgent.exe"; Parameters: "service install"; Flags: runhidden waituntilterminated
Filename: "{app}\AceAgent.exe"; Parameters: "tray"; Flags: nowait postinstall skipifsilent
```

Use `[Registry]` HKLM Run for `"{app}\AceAgent.exe" tray`. Stop/remove idempotently during uninstall. Add a purge task that removes `{commonappdata}\AceITCenter`; normal uninstall preserves it.

- [ ] **Step 4: Add a build script with exact inputs**

`scripts/build-windows-agent.sh VERSION COMMIT BUILT_AT OUT_DIR` requires `ACE_UPDATE_PUBLIC_KEY`, cross-compiles with `-H windowsgui` and `-X` build metadata including the update public key, runs ISCC in normal mode, checks artifact existence, and prints SHA-256. `--agent-only` is an explicit local compile mode; normal mode must fail clearly when ISCC is unavailable.

- [ ] **Step 5: Run contract and cross-build tests**

Run locally:

```bash
scripts/build-windows-agent.test.sh
ACE_UPDATE_PUBLIC_KEY="$(printf test-public-key | base64 -w0)" scripts/build-windows-agent.sh --agent-only 0.2.0 "$(git rev-parse --short=12 HEAD)" "$(date -u +%FT%TZ)" /tmp/ace-release
file /tmp/ace-release/AceAgent.exe
```

Expected: PE32+ x86-64 GUI executable. Task 9's pinned Docker/Wine builder must run the same script without `--agent-only`, compile the Inno installer, and inspect its contents.

- [ ] **Step 6: Commit Task 6**

```bash
git add .gitignore installer/windows scripts/build-windows-agent.sh scripts/build-windows-agent.test.sh
git commit -m "feat: 添加 Windows Agent 安装与卸载包"
```

---

### Task 7: Implement Signed Release Manifests and Release Tooling

**Files:**
- Create: `agent/internal/update/manifest.go`
- Create: `agent/internal/update/manifest_test.go`
- Create: `tools/cmd/ace-release/main.go`
- Create: `tools/cmd/ace-release/main_test.go`

**Interfaces:**
- Produces: `update.Manifest`, `CanonicalPayload`, `Sign`, `Verify`, and `ValidateCandidate`.
- Produces CLI: `ace-release keygen`, `sign`, and `verify`.
- Uses stable channel and schema 1 only.

- [ ] **Step 1: Add failing canonical signature tests**

```go
func TestManifestSignatureCoversAllReleaseFields(t *testing.T) {
    public, private, _ := ed25519.GenerateKey(rand.Reader)
    manifest := validManifest()
    signed, err := Sign(manifest, private)
    if err != nil { t.Fatal(err) }
    if err := Verify(signed, public); err != nil { t.Fatal(err) }
    signed.Size++
    if err := Verify(signed, public); err == nil { t.Fatal("tampered manifest verified") }
}
```

Also test invalid schema/channel, non-semver versions, downgrade/equal versions, wrong OS floor, zero size, malformed SHA-256, bad base64, and cross-origin URL rejection.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./agent/internal/update -run 'TestManifest' -count=1`

- [ ] **Step 3: Implement the exact manifest model**

```go
type Manifest struct {
    Schema int `json:"schema"`
    Channel string `json:"channel"`
    Version string `json:"version"`
    PublishedAt time.Time `json:"published_at"`
    MinimumOS string `json:"minimum_os"`
    URL string `json:"url"`
    Size int64 `json:"size"`
    SHA256 string `json:"sha256"`
    Signature string `json:"signature"`
}
```

Canonical payload is deterministic JSON over every field except `Signature`. Prefix versions with `v` only for `semver.Compare`; preserve manifest JSON without a prefix.

- [ ] **Step 4: Implement the release CLI**

`keygen` writes a 0600 private key and a public key file. `sign` reads installer bytes, computes exact size/hash, signs the manifest, writes atomically, and never prints the private key. `verify` validates signature and artifact.

- [ ] **Step 5: Run focused and full tests**

```bash
go test ./agent/internal/update ./tools/cmd/ace-release -count=1
go test ./... -count=1
```

- [ ] **Step 6: Commit Task 7**

```bash
git add go.mod go.sum agent/internal/update/manifest.go agent/internal/update/manifest_test.go tools/cmd/ace-release
git commit -m "feat: 添加 Agent 更新签名清单"
```

---

### Task 8: Add Silent Update Staging, Health Validation, and Rollback

**Files:**
- Create: `agent/internal/update/checker.go`
- Create: `agent/internal/update/checker_test.go`
- Create: `agent/internal/update/helper.go`
- Create: `agent/internal/update/helper_test.go`
- Create: `agent/internal/update/helper_windows.go`
- Create: `agent/internal/update/helper_nonwindows.go`
- Modify: `agent/internal/controller/controller.go`
- Modify: `agent/cmd/ace-agent/main.go`

**Interfaces:**
- Produces: `update.Checker.Check(ctx) (Candidate, error)` and `Stage(ctx, Candidate) (StagedUpdate, error)`.
- Produces: `update.RunHelper(ctx, HelperOptions) error`.
- Consumes: manifest verifier from Task 7 and Service/pipe health from Task 4.

- [ ] **Step 1: Add failing checker tests**

Use `httptest.Server` to cover signed newer update, invalid signature, size overflow, hash mismatch, unrelated redirect, timeout, equal/downgrade version, and partial download cleanup.

- [ ] **Step 2: Run checker tests and verify RED**

Run: `go test ./agent/internal/update -run 'TestChecker|TestStage' -count=1`

- [ ] **Step 3: Implement bounded check/download behavior**

Limit manifest to 64 KiB and installer to `manifest.Size` with a hard 256 MiB ceiling. Use a dedicated HTTP client that rejects cross-origin redirects. Write `.partial`, fsync, verify, then rename into staging.

- [ ] **Step 4: Add failing rollback state-machine tests**

```go
func TestHelperRestoresLastKnownGoodWhenHealthCheckFails(t *testing.T) {
    ops := fakeUpdateOps{healthErr: errors.New("pipe unavailable")}
    err := RunHelper(context.Background(), testHelperOptions(&ops))
    if err == nil { t.Fatal("expected update failure") }
    if !ops.restored || !ops.oldServiceStarted { t.Fatalf("ops=%#v", ops) }
}
```

- [ ] **Step 5: Implement helper sequencing**

The Service copies `AceAgent.exe` to a temporary helper path and starts `update-helper` detached with paths but no credentials. The helper stops Service, stores last-known-good, runs installer with `/VERYSILENT /SUPPRESSMSGBOXES /NORESTART`, starts Service, waits up to 60 seconds for pipe status, and restores/restarts old binary on failure.

- [ ] **Step 6: Schedule checks in the controller**

Check once after enrollment/startup and every hour plus a random 0..10 minute jitter. One update may run at a time. Manual tray check uses the same mutex and pipeline.

- [ ] **Step 7: Run focused, full, and Windows compile checks**

```bash
go test ./agent/internal/update ./agent/internal/controller -count=1
go test ./... -count=1
GOOS=windows GOARCH=amd64 go build -ldflags="-H windowsgui" -o /tmp/AceAgent.exe ./agent/cmd/ace-agent
```

- [ ] **Step 8: Commit Task 8**

```bash
git add agent/cmd/ace-agent/main.go agent/internal/controller agent/internal/update
git commit -m "feat: 支持 Agent 静默升级与自动回滚"
```

---

### Task 9: Build and Publish Windows Releases from Disposable DSM Docker

**Files:**
- Create: `deploy/windows-builder.Dockerfile`
- Create: `deploy/windows-builder.compose.yaml`
- Create: `scripts/publish-windows-release.sh`
- Create: `scripts/publish-windows-release.test.sh`
- Modify: `deploy/compose.yaml`
- Modify: `deploy/nginx.conf`
- Modify: `deploy/.env.example`
- Modify: `deploy/README.md`
- Modify: `.gitignore`

**Interfaces:**
- Produces persistent artifacts under `${ACE_RELEASES_DIR}/windows/stable/`.
- Produces stable alias `AceAgentSetup-windows-amd64.exe` and `latest.json`.
- Consumes signing key from `${ACE_UPDATE_SIGNING_KEY}` read-only.

- [ ] **Step 1: Add failing publisher shell tests**

The test creates a temporary release root, fake signed manifest/artifact, invokes the script, and asserts versioned artifact, stable alias, manifest, atomic temp cleanup, and refusal to overwrite a newer manifest with an older version.

- [ ] **Step 2: Run the publisher test and verify RED**

Run: `scripts/publish-windows-release.test.sh`

- [ ] **Step 3: Implement the pinned builder image**

Use explicit build args for Go and Inno Setup versions. Install Wine and Inno into the image, compile `ace-release`, derive the public key from the read-only private key, pass it as `ACE_UPDATE_PUBLIC_KEY` to the Agent build, run ISCC, sign/verify the manifest, inspect the real installer inventory, and copy only final artifacts to `/out`. Never print the private key or place it in an image layer or artifact.

- [ ] **Step 4: Add one-shot Compose configuration**

```yaml
services:
  windows-builder:
    build:
      context: ..
      dockerfile: deploy/windows-builder.Dockerfile
    environment:
      RELEASE_VERSION: ${RELEASE_VERSION:?RELEASE_VERSION is required}
    volumes:
      - ${ACE_RELEASES_DIR:-/volume4/docker/docker/ace-it-center/releases}:/out
      - ${ACE_UPDATE_SIGNING_KEY:-/volume4/docker/docker/ace-it-center/secrets/update-signing.key}:/run/secrets/update-signing.key:ro
```

The documented command must use `docker compose ... run --rm windows-builder`.

- [ ] **Step 5: Mount release artifacts into Web**

Add a read-only web volume from `${ACE_RELEASES_DIR}/windows` to `/usr/share/nginx/html/downloads/windows`. Add Nginx `application/octet-stream`, `nosniff`, and cache rules: immutable for versioned EXE, `no-cache` for `latest.json` and stable alias.

- [ ] **Step 6: Implement atomic publication**

Validate the signed manifest before changing `stable`. Copy into a version temp directory, fsync, rename, then atomically replace alias and manifest symlink/file. Never expose partial artifacts.

- [ ] **Step 7: Run shell tests and Compose validation**

```bash
scripts/publish-windows-release.test.sh
docker compose -f deploy/windows-builder.compose.yaml config
docker compose -f deploy/compose.yaml config
```

- [ ] **Step 8: Commit Task 9**

```bash
git add .gitignore deploy/windows-builder.Dockerfile deploy/windows-builder.compose.yaml deploy/compose.yaml deploy/nginx.conf deploy/.env.example deploy/README.md scripts/publish-windows-release.sh scripts/publish-windows-release.test.sh
git commit -m "feat: 添加 DSM Windows Agent 发布流水线"
```

---

### Task 10: Integrate the Installer Workflow into the Web UI

**Files:**
- Modify: `frontend/src/components/AgentDownloads.vue`
- Modify: `frontend/src/components/AgentDownloads.test.ts`
- Modify: `frontend/src/components/OperationsWorkspace.vue`
- Modify: `frontend/src/components/OperationsWorkspace.test.ts`
- Modify: `frontend/src/style.css`

**Interfaces:**
- Windows download becomes `/downloads/windows/stable/AceAgentSetup-windows-amd64.exe`.
- Linux download remains `/downloads/ace-agent-linux-amd64`.
- Windows enrollment copy directs the user to install first and enter URL/token in tray; no raw EXE command.
- Linux command becomes `chmod +x ... && sudo ./ace-agent-linux-amd64 ...`.

- [ ] **Step 1: Update tests first and verify RED**

Assert exact Windows installer URL/file name and copy containing `Windows 10/11 x64`, `安装后从系统托盘打开 Ace Agent`, and server URL/token entry. Assert no Windows CLI command is rendered. Preserve Linux elevated command assertion.

Run:

```bash
cd frontend
npm test -- src/components/AgentDownloads.test.ts src/components/OperationsWorkspace.test.ts
```

Expected: FAIL against the current raw Windows binary workflow.

- [ ] **Step 2: Implement the installer-oriented copy and link**

Keep exactly two platform rows. Windows displays `AceAgentSetup-windows-amd64.exe`; Linux remains raw Agent. Use existing Element Plus icons and design tokens, no custom SVG.

- [ ] **Step 3: Replace the Windows command result with tray instructions**

After token generation, display the token with a copy button and platform steps. Windows: install, open tray, enter current origin and token. Linux: retain one copyable elevated command. Token is only in DOM after generation and is cleared when dialog closes.

- [ ] **Step 4: Run focused, full, and build checks**

```bash
cd frontend
npm test -- src/components/AgentDownloads.test.ts src/components/OperationsWorkspace.test.ts
npm test
npm run build
```

Expected: 0 failures and no Vue warnings.

- [ ] **Step 5: Commit Task 10**

```bash
git add frontend/src/components/AgentDownloads.vue frontend/src/components/AgentDownloads.test.ts frontend/src/components/OperationsWorkspace.vue frontend/src/components/OperationsWorkspace.test.ts frontend/src/style.css
git commit -m "feat: 接入 Windows Agent 安装器下载流程"
```

---

### Task 11: Release, Deploy, and Validate the Complete Windows Lifecycle

**Files:**
- Verify: all files from Tasks 1-10.
- Deploy: changed repository files to `/volume4/docker/docker/ace-it-center/`.
- Publish: version `0.2.0` under `/volume4/docker/docker/ace-it-center/releases/windows/stable/`.
- Record: `.superpowers/sdd/2026-07-27-windows-agent-lifecycle/task-11-report.md`.

**Interfaces:**
- Produces public installer, manifest, healthy Ace IT Center services, and a real-Windows acceptance checklist.

- [ ] **Step 1: Run repository verification**

```bash
git diff --check
go test ./... -count=1
GOOS=windows GOARCH=amd64 go build -ldflags="-H windowsgui" -o /tmp/AceAgent.exe ./agent/cmd/ace-agent
cd frontend && npm test && npm run build
```

- [ ] **Step 2: Generate the signing key once, before any release build**

If the DSM key does not exist, run `ace-release keygen` into `/volume4/docker/docker/ace-it-center/secrets/update-signing.key` and `.pub`, set the private key to mode 0600, record only the public key fingerprint, and never print/copy the private key into reports. This step must complete before Step 4 so the exact matching public key is embedded into `AceAgent.exe`.

- [ ] **Step 3: Back up remote files and upload exact changed paths**

Create `/volume4/docker/docker/ace-it-center/backup/windows-agent-lifecycle-before-0.2.0-20260727`, preserve current source/deploy/frontend files, and use legacy SCP for DSM compatibility. Do not overwrite PostgreSQL data, `.env`, secrets, or unrelated user files.

- [ ] **Step 4: Build with the disposable DSM builder**

```bash
cd /volume4/docker/docker/ace-it-center/deploy
RELEASE_VERSION=0.2.0 docker compose -f windows-builder.compose.yaml build windows-builder
RELEASE_VERSION=0.2.0 docker compose -f windows-builder.compose.yaml run --rm windows-builder
docker compose -f windows-builder.compose.yaml ps -a
```

Expected: installer/manifest exist and no builder container remains running.

- [ ] **Step 5: Verify artifacts before publication**

Run release CLI verification, SHA-256, PE architecture check, Wine `version` smoke check, and Inno archive listing. Assert the installer contains exactly one runtime `AceAgent.exe`, Service setup, and tray auto-start metadata.

- [ ] **Step 6: Publish and recreate only affected runtime services**

Publish release atomically, rebuild/recreate Web only when its source/config changed, and leave PostgreSQL data untouched.

```bash
scripts/publish-windows-release.sh 0.2.0 /volume4/docker/docker/ace-it-center/releases/windows
cd deploy
docker compose build web
docker compose up -d --no-deps --force-recreate web
docker compose ps
```

- [ ] **Step 7: Validate DSM and public routes**

Verify API health plus:

```text
/downloads/windows/stable/latest.json
/downloads/windows/stable/AceAgentSetup-windows-amd64.exe
/downloads/windows/stable/AceAgentSetup-0.2.0-windows-amd64.exe
```

Check status 200, correct content types/cache headers, manifest signature, exact content length/hash, and Web bundle references to the installer.

- [ ] **Step 8: Complete real Windows 10/11 acceptance**

On the user's Windows device, record evidence for install/UAC, no console flash, Service delayed auto-start, tray enrollment, node appearance, heartbeat after tray exit/logoff/reboot, logs, diagnostics, restart, silent upgrade simulation, failure rollback, uninstall, and purge. Any failed item blocks completion.

- [ ] **Step 9: Final cleanup and commit verification**

Confirm builder containers are absent. Builder image/cache may be removed only after artifact verification. Keep installer, manifest, releases, and signing key. Confirm repository worktree is clean and all task commits are present.

---

## Final Verification Matrix

| Area | Required Evidence |
| --- | --- |
| Shared Agent | `go test ./...` passes; Linux flags remain compatible |
| Windows build | GUI PE32+ x64 binary compiles with injected version |
| Service | SCM install/start/stop/recovery acceptance passes |
| Tray | no console flash; one instance; enrollment/status/actions work |
| Security | no token/credential in logs, config projection, IPC response, process args, or diagnostics |
| Installer | install/upgrade/uninstall/purge contracts pass |
| Update | signature/hash/downgrade/redirect checks and rollback tests pass |
| DSM builder | `run --rm` leaves no running/stopped builder container |
| Public release | installer and signed manifest return 200 with exact hash/headers |
| Web | installer workflow and Linux fallback tests/build pass |
| Real Windows | reboot, logoff, silent update, rollback, and uninstall acceptance recorded |
