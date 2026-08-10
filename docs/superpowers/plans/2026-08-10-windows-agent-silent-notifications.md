# Windows Agent Silent Notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 发布 Windows Agent `V0.4.10`，彻底取消 Agent 的 Windows 弹窗通知，同时保留托盘状态、窗口内操作反馈、本地日志、服务端日志上传和自动静默更新。

**Architecture:** 从平台无关的 `View` 中删除已无用途的通知模型和冷却状态，使 Service 状态轮询只负责图标、文字和操作权限。Windows 托盘运行时使用一个始终可见的窗口消息栏显示手动操作结果和安全错误，生产代码不再调用 `walk.NotifyIcon.ShowInfo` 或 `ShowError`。发布继续复用现有一次性 DSM Windows builder、签名清单和原子 stable 切换流程，不修改 Backend、Web、数据库或 Compose 常驻服务。

**Tech Stack:** Go、`github.com/lxn/walk`、Go tests、Bash contract tests、Docker Compose、Inno Setup、Ed25519 release manifest、DSM Container Manager。

## Global Constraints

- 目标 Agent 版本必须为 `0.4.10`，对外文件名必须为 `AceAgentSetup-windows-amd64-V0.4.10.exe`。
- 不升级 Backend/Web，不修改数据库 Schema、Backend API、日志上传协议或 DSM PostgreSQL 数据卷。
- 所有 `NotifyIcon.ShowInfo` 和 `NotifyIcon.ShowError` 调用必须从 Windows Agent 生产代码删除。
- 托盘图标、颜色、工具提示、窗口状态、接入、重启 Worker、检查更新、创建诊断、日志和自动静默更新必须保留。
- 手动操作和本地 UI 错误只在客户端窗口常驻消息栏显示；错误使用红色，成功/信息使用普通文字颜色。
- 心跳和更新错误继续写入现有 `agent.log`/`update.log`；首次成功心跳和之后每小时的服务端日志上传行为不得改变。
- DSM 构建必须使用一次性 `windows-builder` 容器；发布后不得留下运行中的 builder 容器。
- DSM 正式服务继续由 `/volume4/docker/docker/ace-it-center/compose.yaml` 项目管理，不创建脱离项目的常驻容器。
- 不输出 DSM、数据库、GitHub、Updater 或签名密钥凭据。

---

### Task 1: 删除展示模型的通知状态

**Files:**
- Modify: `agent/internal/tray/presenter.go`
- Modify: `agent/internal/tray/presenter_test.go`

**Interfaces:**
- Consumes: `controller.Status` 和现有 `Presenter.View(controller.Status) View`。
- Produces: 不含 `Notification` 字段的 `View`；`StatusModel.Apply` 仍返回最新状态，`PollFailed` 仍清除陈旧设备信息与操作权限。

- [ ] **Step 1: 将旧冷却测试替换为静默状态测试**

在 `agent/internal/tray/presenter_test.go` 删除两个通知冷却测试，加入以下覆盖：

```go
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
```

同时保留 `TestPresenterMapsLifecycleStatesAndActions` 对 `error`/`degraded` 红色图标和 `updating` 蓝色图标的断言。

- [ ] **Step 2: 运行测试并确认红灯**

Run: `go test ./agent/internal/tray -run 'TestPresenterKeepsErrorAndUpdateStatusWithoutNotificationData|TestPresenterMapsLifecycleStatesAndActions' -count=1`

Expected: FAIL，序列化结果仍包含 `notification`。

- [ ] **Step 3: 删除通知 DTO 和冷却逻辑**

在 `agent/internal/tray/presenter.go` 中：

```go
type View struct {
	StatusText     string  `json:"status_text"`
	Icon           Icon    `json:"icon"`
	ShowEnrollment bool    `json:"show_enrollment"`
	ServerURL      string  `json:"server_url,omitempty"`
	Version        string  `json:"version,omitempty"`
	NodeID         string  `json:"node_id,omitempty"`
	LastHeartbeat  string  `json:"last_heartbeat,omitempty"`
	Error          string  `json:"error,omitempty"`
	Actions        Actions `json:"actions"`
}
```

删除 `Notification` 类型、`defaultNotificationCooldown`、`View.Notification`、`Presenter.View` 对该字段的赋值、`StatusModel` 的 `now`/`notificationCooldown`/`notificationDisplayed` 字段、`newStatusModel` 和 `filterNotification`。`NewStatusModel` 直接构造：

```go
func NewStatusModel() *StatusModel {
	presenter := NewPresenter()
	return &StatusModel{
		presenter: presenter,
		view:      presenter.View(controller.Status{State: "unavailable"}),
	}
}
```

`StatusModel.Apply` 只更新并返回 `m.view`。状态文案、图标和 Actions 映射保持原样。

- [ ] **Step 4: 运行展示模型测试并确认绿灯**

Run: `go test ./agent/internal/tray -count=1`

Expected: PASS，错误、升级、轮询失败、恢复和配对表单测试全部通过。

- [ ] **Step 5: 提交展示模型变更**

```bash
git add agent/internal/tray/presenter.go agent/internal/tray/presenter_test.go
git commit -m "fix: 移除 Agent 原生通知状态"
```

### Task 2: 将 Windows 操作反馈收口到窗口消息栏

**Files:**
- Modify: `agent/internal/tray/native_windows.go`
- Modify: `agent/internal/tray/presenter_test.go`

**Interfaces:**
- Consumes: Task 1 的无通知 `View`、现有 `trayRuntime.errorLabel` 和 `userFacingError(error) string`。
- Produces: `showWindowMessage(message string, isError bool)`；`showSafeError(message string)` 仅写窗口，不再触发 Windows 通知。

- [ ] **Step 1: 写 Windows 静默通知源码契约测试**

在 `agent/internal/tray/presenter_test.go` 引入 `os` 和 `strings`，加入：

```go
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
```

- [ ] **Step 2: 运行契约测试并确认红灯**

Run: `go test ./agent/internal/tray -run TestWindowsRuntimeUsesOnlyInWindowOperationFeedback -count=1`

Expected: FAIL，源码仍包含 `.ShowInfo(` 或 `.ShowError(`。

- [ ] **Step 3: 改成始终可见的窗口消息栏**

在 `buildWindow` 中把 `errorLabel` 从接入 `GroupBox.Children` 移到该 GroupBox 之后，使在线设备也能看到反馈：

```go
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
Label{AssignTo: &r.errorLabel},
```

删除 `trayRuntime.lastNotification`。从 `applyView` 删除整个 `view.Notification` 分支。加入统一窗口反馈函数：

```go
func (r *trayRuntime) showWindowMessage(message string, isError bool) {
	if message == "" {
		return
	}
	color := walk.RGB(55, 65, 81)
	if isError {
		color = walk.RGB(180, 35, 35)
	}
	_ = r.errorLabel.SetTextColor(color)
	_ = r.errorLabel.SetText(message)
}

func (r *trayRuntime) showSafeError(message string) {
	r.showWindowMessage(message, true)
}
```

将手动操作结果改为：

```go
r.showWindowMessage("已发送配对请求，请在平台确认", false)
r.showWindowMessage(successMessage, false)
r.showWindowMessage(message, false)
r.showWindowMessage("诊断包已创建："+result.Path, false)
```

配对校验/提交失败、状态读取失败、打开平台失败和打开日志目录失败继续调用 `showSafeError`。开始新的配对提交时把消息栏清空，自动状态轮询不得覆盖已有手动操作结果。

- [ ] **Step 4: 格式化并运行托盘测试**

Run: `gofmt -w agent/internal/tray/presenter.go agent/internal/tray/presenter_test.go agent/internal/tray/native_windows.go`

Run: `go test ./agent/internal/tray -count=1`

Expected: PASS，源码契约确认无原生通知调用，托盘状态模型与窗口生命周期测试均通过。

- [ ] **Step 5: 交叉编译 Windows Agent**

Run: `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o /tmp/ace-agent-silent/AceAgent.exe ./agent/cmd/ace-agent`

Expected: PASS，并生成非空 PE32+ Windows GUI 可执行文件；后续构建脚本测试继续验证 GUI subsystem。

- [ ] **Step 6: 提交 Windows UI 变更**

```bash
git add agent/internal/tray/native_windows.go agent/internal/tray/presenter_test.go
git commit -m "fix: 静默 Windows Agent 弹窗"
```

### Task 3: 回归测试并发布 Agent V0.4.10

**Files:**
- Modify: `deploy/README.md`
- Runtime artifact: `/volume4/docker/docker/ace-it-center/releases/windows/stable/releases/0.4.10/AceAgentSetup-windows-amd64-V0.4.10.exe`
- Runtime manifest: `/volume4/docker/docker/ace-it-center/releases/windows/stable/latest.json`

**Interfaces:**
- Consumes: 已提交的静默通知代码、DSM update signing key、`deploy/windows-builder.compose.yaml`、`scripts/publish-windows-release.sh`。
- Produces: 已签名 stable `0.4.10` 安装包和 manifest；网页 Agent 最新版本展示 `V0.4.10`。

- [ ] **Step 1: 执行本地可用的完整回归**

Run: `go test ./...`

Run: `bash scripts/build-windows-agent.test.sh`

Run: `bash scripts/publish-windows-release.test.sh`

Expected: 所有 Go 测试、Windows Agent 构建契约和 Windows release 发布契约通过。若本机没有 Go/Docker，只记录环境限制并在 DSM builder 中执行同等检查，不跳过最终验证。

- [ ] **Step 2: 推送已验证源码**

Run: `git status --short --branch`

Run: `git push origin main`

Expected: `origin/main` 包含设计、计划和静默通知实现提交；工作区无意外文件被推送。

- [ ] **Step 3: 在 DSM 项目目录同步精确提交**

Run: `ssh -p 9099 jarvis@ace-station.top 'cd /volume4/docker/docker/ace-it-center && git fetch origin main && git merge --ff-only origin/main'`

Expected: DSM 源码 fast-forward 到刚推送的 revision；不得输出 `.env` 或 secret 内容。

- [ ] **Step 4: 运行 DSM 一次性 builder 发布 0.4.10**

在 DSM `/volume4/docker/docker/ace-it-center` 中设置 `RELEASE_VERSION=0.4.10`、当前 Git revision 和 UTC 构建时间，然后执行：

```bash
sudo --preserve-env=RELEASE_VERSION,RELEASE_COMMIT,RELEASE_BUILT_AT \
  docker compose -f deploy/windows-builder.compose.yaml build
sudo --preserve-env=RELEASE_VERSION,RELEASE_COMMIT,RELEASE_BUILT_AT \
  docker compose -f deploy/windows-builder.compose.yaml run --rm windows-builder
```

Expected: builder 内完成 Go 构建、真实 Inno Setup 打包、旧版本覆盖契约、签名验证和 stable 原子发布；命令结束后无运行中的 `ace-it-center-windows-builder` 容器。

- [ ] **Step 5: 验证公开 stable 发布物**

读取 `http://it.ace-station.top:1111/downloads/windows/stable/latest.json`，只输出公开字段，验证：

```text
version = 0.4.10
url = /downloads/windows/stable/AceAgentSetup-windows-amd64-V0.4.10.exe
```

下载该 URL，验证 HTTP 200、文件名、响应大小和本地 SHA-256 与 manifest 的 `size`/`sha256` 完全一致。再验证 stable alias `AceAgentSetup-windows-amd64.exe` 返回同一文件。

- [ ] **Step 6: 验证 DSM Compose 与 Agent 状态**

Run: `ssh -p 9099 jarvis@ace-station.top 'cd /volume4/docker/docker/ace-it-center && sudo docker compose ps'`

Expected: `postgres`、`backend`、`web`、`updater` 仍属于 `ace-it-center` Compose 项目并 healthy；Backend/Web 镜像和版本未因本次 Agent 发布改变。

通过现有受认证设备 API 检查 1502/412：等待既有 `1 小时 + 0～10 分钟随机抖动`周期后，在线设备上报版本应变为 `0.4.10`。离线或休眠设备只记录为待观察，不把未连接误判为自动更新失败。

- [ ] **Step 7: 记录发布证据并提交**

在 `deploy/README.md` 发布记录末尾加入 `0.4.10` 的 Git revision、安装包文件名、公开 SHA-256/大小、测试结果、DSM 验证时间、Compose health 和真实客户端待观察项，不记录任何凭据。

```bash
git add deploy/README.md
git commit -m "docs: 记录 Agent 0.4.10 发布"
git push origin main
```

Expected: GitHub 与 DSM 都可追溯到同一 Agent release revision，发布记录准确且不含敏感信息。

### Task 4: 最终验收

**Files:**
- Verify only: `agent/internal/tray/presenter.go`
- Verify only: `agent/internal/tray/native_windows.go`
- Verify only: `agent/internal/app/worker.go`
- Verify only: `agent/cmd/ace-agent/main.go`

**Interfaces:**
- Consumes: stable `0.4.10` 安装包、现有自动更新与日志上传链路。
- Produces: 静默通知功能的代码、构建、线上发布和客户端观察证据。

- [ ] **Step 1: 做发布后静态和测试复核**

Run: `rg -n '\.ShowInfo\(|\.ShowError\(' agent`

Expected: 无匹配。

Run: `go test ./...`

Expected: PASS。

- [ ] **Step 2: 验证客户端行为边界**

在已升级的 Windows 设备执行以下人工验收：心跳失败时托盘变红但不弹窗；手动检查更新、重启 Worker、创建诊断和配对时无弹窗且窗口消息栏显示结果；`agent.log`/`update.log` 仍记录相关错误；设备恢复在线后心跳和远程命令仍正常。

Expected: 自动状态与手动操作均不产生 Windows 弹窗，诊断信息未丢失，自动更新、命令和日志上传不受影响。
