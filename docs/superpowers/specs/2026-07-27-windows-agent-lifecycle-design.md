# Ace Agent Windows Lifecycle Design

**Date:** 2026-07-27

**Status:** Approved for planning
**Target:** Windows 10/11 x64

## 1. Goal

Turn the current foreground CLI agent into a complete Windows lifecycle while keeping Ace IT Center's management experience in the Web platform.

The Windows delivery must provide:

- one installed `AceAgent.exe` with multiple runtime modes;
- a LocalSystem Windows Service that starts automatically;
- a per-user tray process for enrollment, status, logs, restart, and update actions;
- an Inno Setup installer and uninstaller;
- signed, unattended automatic updates;
- rotating logs and a redacted diagnostic bundle;
- a disposable DSM Docker builder that publishes release artifacts without remaining online.

The client is not a full desktop management application. Device management, jobs, software, monitoring, and policy remain in Ace IT Center Web.

## 2. Confirmed Decisions

- Enrollment happens after installation in the tray UI.
- The user enters both the Ace IT Center server URL and a one-time Enrollment Token.
- Updates install silently in the background.
- The first release has no commercial Authenticode certificate.
- Update authenticity uses an embedded Ed25519 public key plus artifact SHA-256 verification.
- Supported systems are Windows 10/11 x64 only.
- Installation uses Inno Setup.
- Builds run in a disposable Docker/Wine builder on DSM.
- The installed runtime uses a single executable with multiple modes.

## 3. Runtime Architecture

The installer places one main binary at:

```text
C:\Program Files\Ace IT Center\AceAgent.exe
```

The same binary supports these modes:

```text
AceAgent.exe service
AceAgent.exe tray
AceAgent.exe diagnose
AceAgent.exe version
AceAgent.exe service install
AceAgent.exe service uninstall
AceAgent.exe update-helper
```

Mode parsing is a thin entry boundary. Business logic remains in focused packages so Service, tray, update, logging, and enrollment code do not become one monolithic `main.go`.

### 3.1 Process Model

- Windows SCM starts `AceAgent.exe service` as LocalSystem with automatic delayed start.
- HKLM Run starts `AceAgent.exe tray` for each interactive user session.
- Service and tray are separate processes even though they use the same binary.
- Running `AceAgent.exe` without arguments in an interactive session opens the tray status/settings window.
- The Windows binary uses the GUI subsystem to avoid a flashing console window. CLI modes attach to the parent console when one exists.
- Linux keeps its existing CLI behavior and does not include Windows-only UI or Service code.

### 3.2 Package Boundaries

```text
agent/cmd/ace-agent          mode dispatch only
agent/internal/runtime      heartbeat worker lifecycle
agent/internal/config       atomic config and bootstrap state
agent/internal/logging      file and Windows Event Log sinks
agent/internal/windows      Service, SCM, session, and console adapters
agent/internal/ipc          Named Pipe protocol and ACLs
agent/internal/tray         native tray and enrollment dialog
agent/internal/update       manifest, verification, staging, and rollback
agent/internal/diagnostics  redacted diagnostic archive
```

Windows-specific files use build tags. Shared packages stay testable on Linux.

### 3.3 Technology Choices

- Windows Service and SCM integration: `golang.org/x/sys/windows/svc` and `svc/mgr`.
- Windows Named Pipe transport: `github.com/Microsoft/go-winio`.
- Native tray icon and settings dialog: `github.com/lxn/walk`.
- File log rotation: `gopkg.in/natefinch/lumberjack.v2`.
- Update signatures and hashes: Go standard library `crypto/ed25519` and `crypto/sha256`.
- Semantic version comparison: `golang.org/x/mod/semver`.

These libraries stay behind internal interfaces so Windows UI and OS APIs do not leak into the heartbeat worker.

## 4. Installation and Uninstallation

The Inno Setup package is named `AceAgentSetup-<version>-windows-amd64.exe`.

Installation requires elevation and performs these steps:

1. Stop the existing Ace Agent service when upgrading.
2. Install `AceAgent.exe` under Program Files.
3. Install the `AceITCenterAgent` LocalSystem service.
4. Configure automatic delayed start and recovery actions.
5. Add the tray command to HKLM Run.
6. Preserve existing ProgramData configuration during upgrades.
7. Start the service and current user's tray process.

The installer does not ask for the server URL or token. Enrollment belongs to the tray after installation.

Normal uninstall removes the Service, auto-start entry, installed binary, and shortcuts. It preserves ProgramData configuration and logs so reinstall and support diagnosis remain possible. A separate clearly labeled `--purge`/installer option removes ProgramData after stopping the Service.

## 5. Enrollment and Local IPC

Before enrollment, the Service runs in a waiting state and exposes local status through a Windows Named Pipe.

The tray's first-run dialog contains:

- server URL, prefilled with `http://it.ace-station.top:1111` but editable;
- Enrollment Token;
- a connect action with visible progress and error state.

The tray sends enrollment input to the Service over the pipe. The Service:

1. validates URL syntax and token length;
2. verifies that the ProgramData directory is writable before consuming the token;
3. collects enrollment inventory;
4. calls the existing `/api/v1/agent/enroll` endpoint;
5. atomically saves server URL, node ID, and credential;
6. clears the token from memory;
7. starts the heartbeat worker;
8. returns sanitized status to the tray.

The token is never written to config, logs, diagnostics, command-line arguments, or update metadata.

### 5.1 Named Pipe Protocol

The pipe name is `\\.\pipe\AceITCenterAgent`.

The protocol uses bounded JSON request/response messages. Initial methods are:

- `status.get`
- `enrollment.submit`
- `worker.restart`
- `update.check`
- `diagnostics.create`

Messages are limited to 64 KiB and have per-request timeouts. The pipe ACL grants full access to LocalSystem and Administrators and limited request access to interactive authenticated users. Responses never return the stored agent credential.

## 6. Service and Tray Behavior

### 6.1 Service States

```text
waiting_for_enrollment
connecting
online
degraded
updating
stopped
```

The Service continues running when no user is logged in. Heartbeats use the existing 30-second interval, with bounded retry backoff after network failures.

Service recovery is configured to restart after unexpected exits. Normal shutdown cancels in-flight work and flushes logs.

### 6.2 Tray Experience

The tray uses a native Windows library rather than a browser or Electron runtime. It shows:

- current Service and enrollment state;
- server address, agent version, node ID, and last successful heartbeat;
- enrollment form while unregistered;
- actions to open Ace IT Center, open the log directory, check for updates, create diagnostics, and restart the worker;
- update/error notifications without stopping the Service.

Exiting the tray does not stop the Service. Only one tray instance runs per user session.

## 7. Logging and Diagnostics

Detailed logs are JSON lines under:

```text
C:\ProgramData\AceITCenter\logs\agent.log
```

Files rotate at 10 MiB, retain seven files, and expire after 14 days. Windows Event Log receives only Service lifecycle and high-severity failures.

Every log event has a component, level, event name, timestamp, and safe metadata. Tokens and credentials are always redacted.

`AceAgent.exe diagnose` and the tray diagnostic action create a ZIP containing:

- version and build metadata;
- sanitized configuration without credentials;
- Service status;
- recent redacted logs;
- OS and network summary;
- update state.

The diagnostic archive is written to a user-selected path or Desktop and is never uploaded automatically.

## 8. Automatic Update Design

The Service checks for updates after enrollment/startup and every six hours with random jitter.

The stable manifest is served from:

```text
/downloads/windows/stable/latest.json
```

Manifest fields are:

```json
{
  "schema": 1,
  "channel": "stable",
  "version": "0.2.0",
  "published_at": "2026-07-27T00:00:00Z",
  "minimum_os": "10.0.17763",
  "url": "/downloads/windows/stable/AceAgentSetup-0.2.0-windows-amd64.exe",
  "size": 12582912,
  "sha256": "hex",
  "signature": "base64-ed25519"
}
```

The signature covers a canonical payload containing every field except `signature`. The Service embeds only the public key.

Update flow:

1. Fetch the bounded manifest over the configured Ace IT Center origin.
2. Verify schema, channel, semantic version, OS floor, and Ed25519 signature.
3. Download to a ProgramData staging directory with size and timeout limits.
4. Verify exact size and SHA-256.
5. Copy the current executable as the last-known-good binary.
6. Start `update-helper` from a temporary copy.
7. Stop the Service, run the Inno installer silently, and start the Service.
8. Wait for Named Pipe readiness and a healthy worker state.
9. Delete staging/backup files on success or restore the last-known-good binary on failure.

An invalid signature, hash mismatch, downgrade, failed health check, or unsupported OS aborts the update and leaves the current version running.

## 9. Release Build and DSM Publishing

The repository adds a pinned Windows builder image containing:

- Go toolchain;
- Wine;
- Inno Setup compiler;
- resource compiler/assets;
- release signing utility.

The one-shot build command writes artifacts to a mounted output directory and removes its container on exit:

```text
docker compose -f deploy/windows-builder.compose.yaml run --rm windows-builder
```

Persistent DSM paths:

```text
/volume4/docker/docker/ace-it-center/releases/windows/
/volume4/docker/docker/ace-it-center/secrets/update-signing.key
```

The Ed25519 private key is mounted read-only, never copied into an image, artifact, log, or repository. The public key is compiled into the Agent.

Nginx serves the release directory through a read-only bind mount. Publishing a new Agent release does not require rebuilding the main Ace IT Center Web image.

After a successful build, the one-shot container is already removed. The builder image and BuildKit cache may also be deleted; published artifacts remain available.

## 10. Security and Failure Handling

- No Authenticode certificate is required for v1, so SmartScreen may warn during first installation.
- Silent updates never trust TLS alone; manifest signature and artifact hash are mandatory.
- Credentials use an atomic ProgramData file restricted to System and Administrators.
- Enrollment input is bounded and never echoed into logs.
- IPC requests use ACLs, size limits, timeouts, and explicit method allowlists.
- Update downloads reject every cross-origin redirect in this version.
- The Service keeps operating during tray crashes or user logoff.
- Network failures use capped exponential backoff and do not crash the Service.
- Corrupt config places the Service in a degraded state and preserves the file for diagnostics.
- Installer/update failure retains or restores the previous working version.

## 11. Testing Strategy

### 11.1 Automated Tests

- Unit tests for mode dispatch, Service state transitions, config migration, URL/token validation, and worker restart.
- Client/server tests for enrollment and heartbeat compatibility.
- Named Pipe protocol tests using a transport abstraction, including ACL configuration and message limits.
- Tray presenter tests independent of native controls.
- Update tests for version comparison, canonical signing, invalid signatures, hashes, downgrade rejection, staging, rollback, and jitter.
- Logging tests for rotation configuration and secret redaction.
- Diagnostic archive tests confirming credentials/tokens are absent.
- Installer script validation and artifact inventory checks.
- Existing Linux Agent, backend, and frontend tests remain green.

### 11.2 Build Verification

- `go test ./...` on Linux for shared behavior.
- Windows compile gates for every Windows-only package and command.
- Wine smoke checks for `version`, manifest verification, and installer startup/unpack.
- SHA-256, PE architecture, installer contents, and manifest signature verification.

### 11.3 Real Windows Acceptance

Wine cannot validate SCM, UAC, startup, or interactive tray behavior. Final acceptance on a real Windows 10/11 x64 device must verify:

1. double-click install and SmartScreen handling;
2. Service installation, delayed startup, and recovery;
3. tray startup without console flash;
4. server/token enrollment and Web node appearance;
5. heartbeat continuity after logoff and reboot;
6. log/diagnostic actions;
7. silent update and rollback failure simulation;
8. uninstall and purge behavior.

## 12. Delivery Sequence

Implementation remains one feature program but is executed in dependency order:

1. Refactor the current worker and version/config model without changing Linux behavior.
2. Add Windows Service hosting, state model, logging, and Named Pipe IPC.
3. Add native tray enrollment and lifecycle actions.
4. Add Inno Setup installer/uninstaller and DSM disposable builder.
5. Add signed manifest publishing, silent updater, health check, and rollback.
6. Integrate the Web download page with the Windows installer and version metadata.
7. Build and publish on DSM, then complete real Windows acceptance.

## 13. Acceptance Criteria

- Double-clicking the installer does not flash and exit.
- A user can install first, then enroll from the tray using server URL and token.
- The Service persists and sends heartbeats without a logged-in user.
- Reboot starts the Service and tray in their correct sessions.
- Logs rotate and diagnostics exclude secrets.
- A valid newer signed release installs silently.
- Invalid, corrupt, downgraded, or unhealthy updates never replace the last-known-good version.
- Uninstall removes runtime integration; purge also removes ProgramData.
- DSM publishes the installer and manifest while no builder container remains running.
- Existing Windows/Linux download routes, backend APIs, and Web management remain compatible or receive an explicit migration.
