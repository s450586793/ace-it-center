# Windows Agent Fixed Updater Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用固定、独立的 `AceAgentUpdater.exe` 替换随机 `.AceAgent-update-helper-*.exe`，补全 Windows 文件元数据，并通过 `V0.4.11 -> V0.4.12` 真实自动更新验证降低杀毒软件误报的链路。

**Architecture:** `AceAgent.exe` 保留立即检查及每 `1 小时 + 0～10 分钟`调度，通过固定 Updater 的同步 `check` 子命令获取并验证更新，再通过 detached `apply` 子命令执行现有 Service 停止、LKG、安装、健康检查和回滚状态机。自动安装把新版 Updater 写为 `AceAgentUpdater.next.exe`；新 Agent 上线后后台有限重试，将其原子替换为固定 `AceAgentUpdater.exe`。

**Tech Stack:** Go 1.26、Windows Service、Inno Setup 6.3.3、Ed25519 release manifest、Windows PE VERSIONINFO、Bash contract tests、Docker/Wine DSM builder、PostgreSQL/Compose smoke checks。

## Global Constraints

- 不使用加壳、压缩壳、代码混淆或关闭杀毒软件。
- 安装目录只公开固定 `AceAgent.exe`、`AceAgentUpdater.exe`，`.next` 只在更新器替换期间短暂存在。
- Updater 不接收 enrollment token、Agent credential 或带 userinfo/query/fragment 的 URL。
- 保留 Ed25519 manifest、同源 URL、256 MiB 安装包上限、SHA-256、LKG 回滚及 60 秒 pipe 健康检查。
- `V0.4.10 -> V0.4.11` 允许最后一次使用旧随机 helper；`V0.4.11 -> V0.4.12` 必须只使用固定 Updater。
- Agent 发布不重建 Backend、Web 或 DSM PostgreSQL，不删除数据库卷、release、`.env` 或签名私钥。
- 没有 Authenticode 证书时只能降低误报，不能承诺所有杀毒软件零提示。
- 每个生产行为变更必须先运行对应失败测试并确认失败原因，再写实现。

---

### Task 1: 明确“无更新”语义和 Updater 进程协议

**Files:**
- Modify: `internal/release/manifest.go`
- Modify: `internal/release/manifest_test.go`
- Modify: `agent/internal/update/checker.go`
- Modify: `agent/internal/update/checker_test.go`
- Create: `agent/internal/update/protocol.go`
- Create: `agent/internal/update/protocol_test.go`

**Interfaces:**
- Produces: `release.CompareVersions(candidate, current string) (int, error)`。
- Produces: `update.ErrNoUpdateAvailable`，仅表示已认证 manifest 的版本不高于当前 Agent。
- Produces: `update.CheckResult{Available, Version, URL, InstallerPath}` 及严格 `EncodeCheckResult` / `DecodeCheckResult`，stdout 上限 `64 KiB`。

- [ ] **Step 1: 为版本比较和无更新结果写失败测试**

在 `internal/release/manifest_test.go` 增加：

```go
func TestCompareVersionsUsesSemanticVersionPrecedence(t *testing.T) {
    tests := []struct {
        candidate string
        current   string
        want      int
    }{
        {candidate: "0.4.11", current: "0.4.10", want: 1},
        {candidate: "0.4.10", current: "0.4.10", want: 0},
        {candidate: "0.4.9", current: "0.4.10", want: -1},
        {candidate: "1.0.0", current: "1.0.0-rc.1", want: 1},
    }
    for _, test := range tests {
        got, err := CompareVersions(test.candidate, test.current)
        if err != nil || got != test.want {
            t.Fatalf("CompareVersions(%q, %q) = %d, %v; want %d", test.candidate, test.current, got, err, test.want)
        }
    }
}

func TestCompareVersionsRejectsInvalidInput(t *testing.T) {
    if _, err := CompareVersions("0.4", "0.4.10"); err == nil {
        t.Fatal("CompareVersions accepted an invalid candidate")
    }
}
```

在 `agent/internal/update/checker_test.go` 增加一个服务器返回当前版本签名 manifest 的测试，断言 `errors.Is(err, ErrNoUpdateAvailable)`；同时保留签名错误必须返回认证错误，不能误归类为无更新。

- [ ] **Step 2: 运行测试并确认 RED**

Run: `go test ./internal/release ./agent/internal/update`

Expected: FAIL，缺少 `CompareVersions`、`ErrNoUpdateAvailable` 和 protocol 类型；不是编译环境或测试夹具错误。

- [ ] **Step 3: 实现语义化版本比较和无更新 sentinel**

在 `internal/release/manifest.go` 增加：

```go
func CompareVersions(candidate, current string) (int, error) {
    if !validSemver(candidate) || !validSemver(current) {
        return 0, errors.New("versions must use valid semantic versioning")
    }
    return semver.Compare("v"+candidate, "v"+current), nil
}
```

让 `ValidateCandidate` 复用该函数。`Checker.Check` 必须先完成 JSON 严格解析和 Ed25519 验证，再比较版本；比较结果 `<= 0` 时返回 `ErrNoUpdateAvailable`。未经认证的 manifest 永远不能返回该 sentinel。

- [ ] **Step 4: 为进程 JSON 协议写失败测试**

在 `protocol_test.go` 覆盖：

```go
func TestCheckResultRoundTrip(t *testing.T) {
    want := CheckResult{
        Available: true,
        Version: "0.4.11",
        URL: "http://it.example:1111/downloads/windows/stable/AceAgentSetup-windows-amd64-V0.4.11.exe",
        InstallerPath: `C:\ProgramData\AceITCenter\updates\AceAgentSetup-windows-amd64-V0.4.11.exe`,
    }
    var output bytes.Buffer
    if err := EncodeCheckResult(&output, want); err != nil {
        t.Fatal(err)
    }
    got, err := DecodeCheckResult(bytes.NewReader(output.Bytes()))
    if err != nil || got != want {
        t.Fatalf("DecodeCheckResult() = %#v, %v; want %#v", got, err, want)
    }
}
```

另外断言未知字段、多个 JSON 值、超过 `64 KiB`、`Available=false` 却带版本/路径、`Available=true` 但版本或绝对安装路径缺失全部失败。

- [ ] **Step 5: 实现严格协议并跑 GREEN**

`protocol.go` 使用 `json.Decoder.DisallowUnknownFields()`、EOF 检查和 `io.LimitReader`；`CheckResult.Validate()` 必须保证无更新结果字段为空，有更新结果版本合法、URL 是公开 HTTP(S) URL且不含 userinfo、`InstallerPath` 为绝对路径。

Run: `go test ./internal/release ./agent/internal/update`

Expected: PASS。

- [ ] **Step 6: 提交 Task 1**

```bash
git add internal/release/manifest.go internal/release/manifest_test.go agent/internal/update/checker.go agent/internal/update/checker_test.go agent/internal/update/protocol.go agent/internal/update/protocol_test.go
git commit -m "refactor: 明确 Agent 更新检查协议"
```

---

### Task 2: 构建固定独立 Updater

**Files:**
- Create: `agent/internal/agentpaths/paths.go`
- Create: `agent/internal/agentpaths/paths_test.go`
- Create: `agent/internal/updaterapp/app.go`
- Create: `agent/internal/updaterapp/app_test.go`
- Create: `agent/cmd/ace-agent-updater/main.go`
- Create: `agent/cmd/ace-agent-updater/main_test.go`
- Modify: `agent/internal/update/helper.go`
- Modify: `agent/internal/update/helper_windows.go`
- Modify: `agent/internal/update/helper_test.go`
- Modify: `agent/cmd/ace-agent/main.go`
- Modify: `agent/cmd/ace-agent/main_test.go`
- Modify: `agent/internal/app/commands.go`
- Modify: `agent/internal/app/commands_test.go`

**Interfaces:**
- Consumes: `update.CheckResult`、`update.Checker`、`update.RunHelper`。
- Produces: `agentpaths.DefaultConfigPath(goos, programData string) string`、`UpdateLogPath(configPath string) string`、`StagingDirectory(configPath string) string`、`UpdaterPath(agentExecutable string) string`、`PendingUpdaterPath(agentExecutable string) string`。
- Produces: `updaterapp.Run(ctx, args, stdout, dependencies) error`，子命令仅为 `check`、`apply`、`version`。
- Changes: 新 Agent 不再支持自身的 `update-helper` mode；`apply` 只允许固定 `AceAgentUpdater.exe` 执行。

- [ ] **Step 1: 为共享路径写失败测试**

```go
func TestWindowsAgentPathsAreStable(t *testing.T) {
    config := DefaultConfigPath("windows", `C:\ProgramData`)
    if config != `C:\ProgramData\AceITCenter\agent.json` {
        t.Fatalf("config path = %q", config)
    }
    agent := `C:\Program Files\Ace IT Center\AceAgent.exe`
    if got := UpdaterPath(agent); got != `C:\Program Files\Ace IT Center\AceAgentUpdater.exe` {
        t.Fatalf("updater path = %q", got)
    }
    if got := PendingUpdaterPath(agent); got != `C:\Program Files\Ace IT Center\AceAgentUpdater.next.exe` {
        t.Fatalf("pending updater path = %q", got)
    }
}
```

Run: `go test ./agent/internal/agentpaths`

Expected: FAIL，package 尚不存在。

- [ ] **Step 2: 实现共享路径并替换 Agent 内重复函数**

`agentpaths` 只负责确定性路径，不读配置内容。Windows `ProgramData` 为空时回退 `C:\ProgramData`；非 Windows 配置仍为 `/etc/ace-it-center/agent.json`。修改 Agent main 使用该 package，删除原来的 `defaultConfigPath`、`serviceLogPath`、`updateLogPath` 重复实现。

Run: `go test ./agent/internal/agentpaths ./agent/cmd/ace-agent`

Expected: PASS。

- [ ] **Step 3: 为 Updater 三个子命令写失败测试**

`updaterapp/app_test.go` 使用 `httptest.Server` 和临时目录，覆盖：

```go
func TestCheckDownloadsAuthenticatedCandidateAndWritesBoundedJSON(t *testing.T) {
    // 服务器返回测试私钥签名的 0.4.11 manifest 与 installer。
    // Run(check) 后 DecodeCheckResult 必须得到 Available=true、0.4.11 和临时安装包绝对路径。
}

func TestCheckReturnsUnavailableForCurrentVersion(t *testing.T) {
    // 签名 manifest 为 0.4.10，current-version 也是 0.4.10。
    // stdout 必须是 {"available":false}，且 error=nil。
}

func TestApplyNeverReceivesCredentials(t *testing.T) {
    args := []string{"apply", "--installer", `/updates/setup.exe`, "--agent", `/program/AceAgent.exe`, "--backup", `/updates/AceAgent.lkg.exe`, "--version", "0.4.11"}
    // 注入 helper runner，断言解析出的 HelperOptions 只有本地路径和公开版本。
}
```

同时测试未知参数、相对路径、额外 positional argument、错误签名、跨源重定向和 `version` 只输出 `buildinfo.Version`。

Run: `go test ./agent/internal/updaterapp ./agent/cmd/ace-agent-updater`

Expected: FAIL，package 和 command 尚不存在。

- [ ] **Step 4: 实现最小 Updater app 和入口**

`check` 参数必须精确为：

```text
check --origin <public-origin> --current-version <semver> --current-os <windows-version> --staging <absolute-path>
```

它创建 `update.Checker`，用注入的 public key 执行 `Check` 和 `Stage`，并只把 `CheckResult` 写 stdout；运行日志写 `update.log`。`apply` 参数必须精确为：

```text
apply --installer <absolute-path> --agent <absolute-path> --backup <absolute-path> --version <semver>
```

它补入可信 staging 路径后调用 `update.RunHelper`。`main.go` 使用 Windows GUI subsystem、`buildinfo.Version` 和 `buildinfo.UpdatePublicKey`，任何错误只写结构化日志并使用非零退出码，不弹窗。

- [ ] **Step 5: 把 helper 身份校验收紧为固定 Updater**

替换随机 helper 身份测试：运行文件最终路径必须与 `filepath.Join(filepath.Dir(installedAgent), "AceAgentUpdater.exe")` 相同，不能是 Agent 本身、hardlink、`.next` 或 staging 目录文件。全局 mutex 必须仍在停止 Service 前获取。

保留 `RunHelper` 的事件顺序断言：

```text
stop -> stop tray -> backup -> install -> apply service config -> start -> health -> cleanup
```

删除新版本 Agent 的 `update-helper` mode 和临时自删除逻辑；旧 `V0.4.10` helper 使用旧二进制完成迁移，不依赖新 Agent 保留该 mode。

- [ ] **Step 6: 跑 Updater 与回滚测试 GREEN**

Run: `go test ./agent/internal/agentpaths ./agent/internal/updaterapp ./agent/internal/update ./agent/cmd/ace-agent-updater ./agent/cmd/ace-agent ./agent/internal/app`

Expected: PASS；既有安装失败、Service 配置失败、健康检查失败和 LKG 恢复测试仍通过。

- [ ] **Step 7: 提交 Task 2**

```bash
git add agent/internal/agentpaths agent/internal/updaterapp agent/cmd/ace-agent-updater agent/internal/update/helper.go agent/internal/update/helper_windows.go agent/internal/update/helper_test.go agent/cmd/ace-agent/main.go agent/cmd/ace-agent/main_test.go agent/internal/app/commands.go agent/internal/app/commands_test.go
git commit -m "feat: 添加固定 Windows Agent 更新器"
```

---

### Task 3: Agent 调度固定 Updater 并提升 `.next`

**Files:**
- Create: `agent/internal/update/client.go`
- Create: `agent/internal/update/client_test.go`
- Create: `agent/internal/update/promote.go`
- Create: `agent/internal/update/promote_test.go`
- Create: `agent/internal/update/promote_windows.go`
- Create: `agent/internal/update/promote_nonwindows.go`
- Modify: `agent/cmd/ace-agent/main.go`
- Modify: `agent/cmd/ace-agent/main_test.go`
- Modify: `agent/internal/controller/controller_test.go`

**Interfaces:**
- Consumes: 固定 Updater `check` / `apply` CLI 和 `update.CheckResult` JSON。
- Produces: `update.ProcessClient.Check(ctx, CheckOptions) (CheckResult, error)` 和 `LaunchApply(ctx, ApplyOptions) error`。
- Produces: `update.PromotePendingUpdater(ctx, PromotionOptions) error`，供 Service Worker 后台调用。

- [ ] **Step 1: 为固定进程客户端写失败测试**

用 fake command runner 覆盖：

```go
func TestProcessClientCheckUsesFixedUpdaterAndPublicArguments(t *testing.T) {
    // 断言 executable 精确为 Program Files\Ace IT Center\AceAgentUpdater.exe。
    // 参数只含 origin/current-version/current-os/staging，不含 credential。
    // stdout 被 DecodeCheckResult 严格解析。
}

func TestProcessClientLaunchApplyIsDetachedAndCredentialFree(t *testing.T) {
    // 断言 apply 参数只有 installer/agent/backup/version，且 Windows flags 为
    // CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS | CREATE_BREAKAWAY_FROM_JOB。
}
```

还要测试 stdout 超限、未知 JSON、Updater 不存在、进程非零退出和 context cancel。

Run: `go test ./agent/internal/update`

Expected: FAIL，`ProcessClient` 尚不存在。

- [ ] **Step 2: 实现进程客户端并替换 Agent 内下载逻辑**

`executeServiceUpdate` 改为：先 `client.Check`；`Available=false` 直接返回空 `UpdateStatus`；有候选时再获取 generation authorization，随后 `LaunchApply`。启动成功才设置 updating/pending；失败删除 staged installer并释放 authorization。

Agent 不再直接构造 `update.Checker`，但 Controller 的立即检查、周期检查、手动检查、single-flight 和 pending TTL 行为保持不变。

- [ ] **Step 3: 为 `.next` 提升写失败测试**

```go
func TestPromotePendingUpdaterRetriesSharingViolationThenReplacesFixedFile(t *testing.T) {
    operations := &fakePromotionOperations{replaceErrors: []error{errSharingViolation, nil}}
    err := PromotePendingUpdater(context.Background(), PromotionOptions{
        AgentVersion: "0.4.11",
        InstalledPath: `/program/AceAgentUpdater.exe`,
        PendingPath: `/program/AceAgentUpdater.next.exe`,
        RetryInterval: time.Millisecond,
        Timeout: 50 * time.Millisecond,
        Operations: operations,
    })
    if err != nil || operations.replaceCalls != 2 {
        t.Fatalf("promotion = %v, calls=%d", err, operations.replaceCalls)
    }
}
```

另测：无 `.next` 是成功 no-op；symlink、目录、版本输出不等于当前 Agent、输出超限、永久占用超时均失败且不删除旧 fixed updater。遗留清理测试只允许删除 staging 目录中精确匹配 `.AceAgent-update-helper-*.exe`、不是当前进程且超过 `24 小时`的普通文件；新文件、symlink、其他目录及相似名称必须保留。

- [ ] **Step 4: 实现安全提升和后台调用**

Promotion 先用 `Lstat` 拒绝 symlink/非普通文件，再执行 `AceAgentUpdater.next.exe version`，要求 bounded stdout 精确等于当前 `buildinfo.Version`。Windows 原子替换使用 `MoveFileEx(MOVEFILE_REPLACE_EXISTING|MOVEFILE_WRITE_THROUGH)`，仅对 sharing/access violation 重试，默认上限 `90 秒`、间隔 `500 ms`。

`serviceWorker.Run` 在心跳 Worker 启动后用同一个 context 后台执行提升；错误写 `agent.log`，不得阻塞心跳、命令或配对。同一个有界维护函数清理超过 `24 小时`的旧随机 helper，禁止 glob 跨出 staging 目录。

- [ ] **Step 5: 跑 Agent 生命周期 GREEN**

Run: `go test ./agent/internal/update ./agent/cmd/ace-agent ./agent/internal/controller ./agent/internal/app`

Expected: PASS；现有 Controller concurrency、shutdown、manual update 和 pending helper tests 全部通过或按固定 Updater 语义重命名后通过。

- [ ] **Step 6: 提交 Task 3**

```bash
git add agent/internal/update/client.go agent/internal/update/client_test.go agent/internal/update/promote.go agent/internal/update/promote_test.go agent/internal/update/promote_windows.go agent/internal/update/promote_nonwindows.go agent/cmd/ace-agent/main.go agent/cmd/ace-agent/main_test.go agent/internal/controller/controller_test.go
git commit -m "refactor: 委托固定更新器执行 Agent 升级"
```

---

### Task 4: 安装器、VERSIONINFO 和构建契约

**Files:**
- Modify: `installer/windows/AceAgent.iss`
- Create: `installer/windows/versioninfo-agent.rc.in`
- Create: `installer/windows/versioninfo-updater.rc.in`
- Modify: `scripts/build-windows-agent.sh`
- Modify: `scripts/build-windows-agent.test.sh`
- Modify: `deploy/windows-builder.Dockerfile`
- Modify: `scripts/publish-windows-release.test.sh`
- Modify: `deploy/README.md`

**Interfaces:**
- Consumes: `agent/cmd/ace-agent` 和 `agent/cmd/ace-agent-updater`。
- Produces: `AceAgent.exe`、`AceAgentUpdater.exe` 和 `AceAgentSetup-windows-amd64-V<version>.exe`。
- Installer behavior: manual/fresh install writes fixed Updater；`/UPDATEHELPER` writes `AceAgentUpdater.next.exe`。

- [ ] **Step 1: 先扩展构建契约并确认 RED**

`scripts/build-windows-agent.test.sh` 必须新增断言：

```text
AceAgentUpdater.exe exists
both PE files are PE32+ GUI x86-64
fake ISCC receives /DSourceUpdater=<absolute path>
installer build fails if updater is absent
UTF-16 strings contain CompanyName/ProductName/FileDescription/ProductVersion/OriginalFilename
neither output contains WinDivert or .sys inventory entries
```

`publish-windows-release.test.sh` 保持 installer manifest 发布原子性，并增加测试输入 inventory 同时要求两个 EXE。

Run: `bash scripts/build-windows-agent.test.sh && bash scripts/publish-windows-release.test.sh`

Expected: FAIL，Updater artifact、`/DSourceUpdater` 和 VERSIONINFO 尚不存在。

- [ ] **Step 2: 生成两个二进制及 Windows VERSIONINFO**

`build-windows-agent.sh` 从 semantic version core 生成四段数值版本，例如 `0.4.11 -> 0,4,11,0`。用两个 `.rc.in` 生成临时 `.rc`，再由 `x86_64-w64-mingw32-windres` 生成临时 COFF `.syso`，通过 Go `-overlay` 注入各自 main package，不能在源码目录遗留生成文件。

字段精确为：

```text
CompanyName       Ace IT Center
ProductName       Ace IT Center Agent
FileDescription   Ace IT Center Agent / Ace IT Center Agent Updater
FileVersion       <major>.<minor>.<patch>.0
ProductVersion    <release semver>
OriginalFilename  AceAgent.exe / AceAgentUpdater.exe
InternalName      AceAgent / AceAgentUpdater
LegalCopyright    Copyright (C) 2026 Ace IT Center
```

Docker builder 安装固定 Debian package `binutils-mingw-w64-x86-64`。`WINDRES_BIN` 可由测试覆盖，缺失时构建必须 fail closed。

- [ ] **Step 3: 修改 Inno Setup 双 Updater 文件策略**

增加必须的 `SourceUpdater` 宏和两个互斥 `[Files]` entry：

```ini
Source: "{#SourceUpdater}"; DestDir: "{app}"; DestName: "AceAgentUpdater.exe"; Flags: ignoreversion; Check: not IsUpdateHelperMode
Source: "{#SourceUpdater}"; DestDir: "{app}"; DestName: "AceAgentUpdater.next.exe"; Flags: ignoreversion; Check: IsUpdateHelperMode
```

补齐安装器 `VersionInfoCompany`、`VersionInfoDescription`、`VersionInfoProductName`、`VersionInfoProductVersion`、`VersionInfoVersion`、`VersionInfoCopyright` 和 `VersionInfoOriginalFileName`。卸载时删除 fixed 和 pending Updater；普通升级保留 `agent.json`。

- [ ] **Step 4: 更新真实 builder inventory**

`deploy/windows-builder.Dockerfile` 在 `innoextract --list` 后同时要求 `AceAgent.exe` 和 `AceAgentUpdater.exe`，并拒绝大小写不敏感的 `WinDivert` 与 `.sys` 文件。Wine 升级契约断言：

```text
AceAgent.exe replaced
AceAgentUpdater.next.exe created in /UPDATEHELPER mode
agent.json byte-for-byte unchanged
```

- [ ] **Step 5: 跑构建契约 GREEN 和 Windows 交叉编译**

Run:

```bash
bash scripts/build-windows-agent.test.sh
bash scripts/publish-windows-release.test.sh
go test ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./agent/cmd/ace-agent
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./agent/cmd/ace-agent-updater
```

Expected: 全部 exit `0`；contract 输出明确 pass count，两个 Windows build 成功。

- [ ] **Step 6: 提交 Task 4**

```bash
git add installer/windows/AceAgent.iss installer/windows/versioninfo-agent.rc.in installer/windows/versioninfo-updater.rc.in scripts/build-windows-agent.sh scripts/build-windows-agent.test.sh deploy/windows-builder.Dockerfile scripts/publish-windows-release.test.sh deploy/README.md
git commit -m "build: 打包固定 Agent 更新器和版本资源"
```

---

### Task 5: 发布迁移版和验证版并完成线上验收

**Files:**
- Modify: `deploy/README.md`
- Modify: `docs/superpowers/specs/2026-08-13-windows-fixed-updater-design.md` only if acceptance evidence reveals a factual correction

**Interfaces:**
- Consumes: DSM `/volume4/docker/docker/ace-it-center` 的一次性 Windows builder、既有签名私钥和 releases volume。
- Produces: stable `V0.4.11` migration installer，随后 stable `V0.4.12` verification installer。

- [ ] **Step 1: 发布前完整验证**

在干净 commit 上运行：

```bash
go test ./...
bash scripts/build-windows-agent.test.sh
bash scripts/publish-windows-release.test.sh
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./agent/cmd/ace-agent
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./agent/cmd/ace-agent-updater
```

在 DSM builder 内重复完整构建契约；任何失败都不得更新 stable manifest。

- [ ] **Step 2: 发布 `V0.4.11` 迁移版**

使用当前精确 Git commit 作为 `RELEASE_COMMIT`，通过 `deploy/windows-builder.compose.yaml` 一次性 builder 构建并原子发布。验证：

```text
latest.json version = 0.4.11
URL filename = AceAgentSetup-windows-amd64-V0.4.11.exe
HTTP 200 Content-Length equals manifest.size
download SHA-256 equals manifest.sha256
stable alias bytes equal versioned installer
no builder container remains running
```

Backend/Web/Updater 镜像保持 `V0.4.2`，Compose 四个服务保持 healthy。

- [ ] **Step 3: 等待真实客户端完成迁移**

按现有最大检查窗口等待最多 `70 分钟`，数据库查询必须显示 `412-itx` 和 `ace-pc` 上报 `0.4.11` 且心跳新鲜。通过服务端上传日志确认 helper completed；随后通过命令中心只读命令确认：

```powershell
Get-Item 'C:\Program Files\Ace IT Center\AceAgentUpdater.exe' |
  Select-Object FullName,Length,VersionInfo
```

不得在文档中记录设备 credential、配对信息或私有命令 token。

- [ ] **Step 4: 发布 `V0.4.12` 验证版**

不改变 Updater 协议，只用下一 release version 构建。重复 manifest、大小、SHA-256、stable alias 和 Compose health 验证。

- [ ] **Step 5: 验证固定 Updater 完成真实自动升级**

等待最多 `70 分钟`，要求两台在线设备上报 `0.4.12`。服务端日志必须显示 fixed Updater 的 check/apply success；只读命令确认：

```powershell
$updates = 'C:\ProgramData\AceITCenter\updates'
Get-ChildItem $updates -Force -Filter '.AceAgent-update-helper-*.exe'
Get-Item 'C:\Program Files\Ace IT Center\AceAgentUpdater.exe' |
  Select-Object FullName,Length,@{N='ProductVersion';E={$_.VersionInfo.ProductVersion}}
```

预期：没有新随机 helper；fixed Updater ProductVersion 为 `0.4.12`；Agent Service running、心跳和命令执行正常。火绒侧是否还有固定 Updater 提示由用户界面最终确认，远程日志不能伪造该结论。

- [ ] **Step 6: 精确清理构建资源**

先列出本次创建的 builder container/image、Go cache volume 和临时源码目录，逐项验证没有容器引用；然后仅删除本次明确名称的资源。不得运行 broad prune、`--force`、通配递归删除，也不得触碰 PostgreSQL volume、release、`.env` 或签名 key。

- [ ] **Step 7: 记录证据并提交发布说明**

在 `deploy/README.md` 记录两个版本的 UTC 发布时间、commit、文件名、size、SHA-256、测试数、客户端迁移时间、固定 Updater 验证和剩余 Authenticode 限制。

```bash
git add deploy/README.md
git commit -m "docs: 记录固定 Agent 更新器发布"
git push origin main
```

- [ ] **Step 8: 最终验证**

Run:

```bash
git status --short --branch
git ls-remote --heads origin main
curl -fsSL http://it.ace-station.top:1111/downloads/windows/stable/latest.json
```

Expected: worktree clean，local `main` 等于 `origin/main`，public manifest 为 `0.4.12`，DSM Compose healthy，两台在线客户端为 `0.4.12`。
