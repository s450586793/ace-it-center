# Ace IT Center Command Center Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付 Owner 从 Web 向 Windows Agent 下发 PowerShell/CMD 命令、Agent 执行并回传持久结果的完整 `V0.4.0` 命令中心。

**Architecture:** PostgreSQL 保存任务与逐设备执行记录，Backend 通过 Owner Session API 管理任务，并通过设备 Credential API 提供 20 秒长轮询领取和带租约的状态回传。Windows Agent 的命令循环与心跳并行运行，使用固定 Shell 启动方式、进程树超时终止和有界纯文本输出；Vue 命令中心直接轮询任务历史和详情。

**Tech Stack:** Go 1.26、Gin、PostgreSQL 16、`database/sql`、Vue 3、TypeScript、Element Plus、Vitest、Docker Compose、Inno Setup。

## Global Constraints

- 目标客户端版本固定为 `V0.4.0`，支持 Windows 10/11 x64。
- 首版只执行 `powershell` 和 `cmd`，Linux Agent 不领取命令。
- 命令长度上限 32 KiB；超时范围 10 至 1800 秒，默认 300 秒；合并输出上限 256 KiB。
- 执行状态只使用 `queued`、`leased`、`running`、`succeeded`、`failed`、`timed_out`。
- Agent 长轮询最多 20 秒；租约有效期 35 分钟；每台设备一次只领取一个命令。
- 所有新行为必须先运行失败测试，再写最小实现并重新运行测试。
- 保留现有配对、心跳、日志上传、网络实时速度、累计流量和自动更新行为。
- 工作树已有连续开发改动；每次提交前只暂存本任务产生且已核对的文件或 hunk，绝不使用 `git add .`。
- DSM 只通过 `/volume4/docker/docker/ace-it-center/compose.yaml` 更新 `postgres`、`backend`、`web` 项目服务。
- `V0.3.8/V0.3.9 -> V0.4.0` 不作为自动更新可靠性证明；可靠性留到真实 `V0.4.0 -> V0.4.1` 验收。

## File Structure

- Create `internal/core/command.go`: 命令枚举、任务、执行、领取和完成传输类型。
- Create `internal/core/command_test.go`: 类型校验和终态判断测试。
- Modify `internal/postgres/schema.sql`: 幂等创建任务表、执行表和领取索引。
- Create `internal/postgres/commands.go`: Owner 查询/创建/重试和 Agent 领取/状态转换。
- Create `internal/postgres/commands_test.go`: SQL 事务、行锁、租约和状态迁移测试。
- Modify `internal/api/repository.go`: 增加命令仓储接口。
- Modify `internal/api/router.go`: 注册和实现 Owner/Agent 命令 API。
- Modify `internal/api/router_test.go`: API 认证、校验、长轮询、幂等和错误映射测试。
- Modify `agent/internal/agent/client.go`: 领取、开始和完成命令的 HTTP Client。
- Modify `agent/internal/agent/client_test.go`: 204、租约冲突和请求负载测试。
- Create `agent/internal/command/executor.go`: Shell 白名单、输出限制和结果映射。
- Create `agent/internal/command/executor_test.go`: 成功、非零、超时、截断和敏感数据测试。
- Create `agent/internal/command/process_windows.go`: Windows 隐藏进程启动和 `taskkill /T /F` 进程树终止。
- Create `agent/internal/command/process_nonwindows.go`: 非 Windows 平台明确返回不支持。
- Create `agent/internal/app/command_worker.go`: 命令领取、开始、执行、完成和退避循环。
- Create `agent/internal/app/command_worker_test.go`: 离线重试、单任务串行和终态回传测试。
- Modify `agent/internal/app/worker.go`: 将命令循环作为不影响心跳状态的后台循环运行。
- Modify `agent/internal/app/worker_test.go`: 命令循环失败不停止心跳测试。
- Modify `agent/cmd/ace-agent/main.go`: Windows Service 注入 Client、Executor 和安全日志。
- Modify `agent/cmd/ace-agent/main_test.go`: Service Worker 命令依赖装配测试。
- Modify `frontend/src/types.ts`: 命令任务和执行类型。
- Create `frontend/src/components/CommandCenter.vue`: 命令表单、历史表和详情抽屉。
- Create `frontend/src/components/CommandCenter.test.ts`: 表单、轮询、详情和重试测试。
- Modify `frontend/src/components/OperationsWorkspace.vue`: 命令中心导航和视图。
- Modify `frontend/src/components/OperationsWorkspace.test.ts`: 导航和生命周期测试。
- Modify `frontend/src/style.css`: 命令编辑器、历史表和日志详情的响应式样式。
- Modify `deploy/README.md`: 记录 `V0.4.0` 发布与验收结果。

---

### Task 1: Core Command Contract

**Files:**
- Create: `internal/core/command.go`
- Create: `internal/core/command_test.go`

**Interfaces:**
- Produces: `CommandShell`, `CommandStatus`, `CommandTask`, `CommandExecution`, `CommandTaskDetail`, `CommandClaim`, `CommandCompletion`。
- Produces: `ValidateCommand(shell CommandShell, command string, timeoutSeconds int) error` 和 `CommandStatus.Terminal() bool`。

- [ ] **Step 1: Write the failing validation tests**

```go
func TestValidateCommandAcceptsWindowsShells(t *testing.T) {
    for _, shell := range []core.CommandShell{core.CommandShellPowerShell, core.CommandShellCMD} {
        if err := core.ValidateCommand(shell, "hostname", 300); err != nil {
            t.Fatalf("ValidateCommand(%q): %v", shell, err)
        }
    }
}

func TestValidateCommandRejectsInvalidBounds(t *testing.T) {
    tests := []struct{ shell core.CommandShell; command string; timeout int }{
        {"shell", "hostname", 300},
        {core.CommandShellPowerShell, "", 300},
        {core.CommandShellCMD, "hostname", 9},
        {core.CommandShellCMD, strings.Repeat("x", 32<<10+1), 300},
    }
    for _, test := range tests {
        if core.ValidateCommand(test.shell, test.command, test.timeout) == nil {
            t.Fatalf("invalid command accepted: %#v", test)
        }
    }
}
```

- [ ] **Step 2: Run the tests and verify RED**

Run: `go test ./internal/core -run 'TestValidateCommand|TestCommandStatus' -count=1`

Expected: FAIL because command types and validation do not exist.

- [ ] **Step 3: Implement the exact core contract**

```go
type CommandShell string
const (
    CommandShellPowerShell CommandShell = "powershell"
    CommandShellCMD CommandShell = "cmd"
)

type CommandStatus string
const (
    CommandQueued CommandStatus = "queued"
    CommandLeased CommandStatus = "leased"
    CommandRunning CommandStatus = "running"
    CommandSucceeded CommandStatus = "succeeded"
    CommandFailed CommandStatus = "failed"
    CommandTimedOut CommandStatus = "timed_out"
)

func ValidateCommand(shell CommandShell, command string, timeoutSeconds int) error
func (status CommandStatus) Terminal() bool

type CommandTask struct {
    ID string; Shell CommandShell; Command string; TimeoutSeconds int
    CreatedBy string; RetriedFromID *string; CreatedAt time.Time
    TargetCount int; Counts CommandStatusCounts
}
type CommandExecution struct {
    ID string; TaskID string; NodeID string; NodeName string; Status CommandStatus
    Attempt int; StartedAt *time.Time; FinishedAt *time.Time; ExitCode *int
    Output string; OutputTruncated bool; ErrorMessage string; DurationMS *int64
}
type CommandTaskDetail struct { Task CommandTask; Executions []CommandExecution }
type CommandClaim struct {
    ExecutionID string; TaskID string; Shell CommandShell; Command string
    TimeoutSeconds int; LeaseToken string; LeaseExpiresAt time.Time
}
type CommandCompletion struct {
    ExecutionID string; LeaseToken string; Status CommandStatus; ExitCode *int
    Output string; OutputTruncated bool; ErrorMessage string; DurationMS int64
}
```

`ValidateCommand` trims only for the empty check while preserving the original command bytes, enforces UTF-8, 32 KiB, the Shell allowlist, and 10..1800 seconds.

- [ ] **Step 4: Run tests and verify GREEN**

Run: `go test ./internal/core -count=1`

Expected: PASS.

- [ ] **Step 5: Commit only the new core files**

```bash
git add internal/core/command.go internal/core/command_test.go
git diff --cached --check
git commit -m "feat: 定义命令任务核心模型"
```

### Task 2: PostgreSQL Task Persistence and Leasing

**Files:**
- Modify: `internal/postgres/schema.sql`
- Create: `internal/postgres/commands.go`
- Create: `internal/postgres/commands_test.go`
- Modify: `internal/postgres/migration_integration_test.go`

**Interfaces:**
- Consumes: Task 1 core types.
- Produces: `CreateCommand`, `ListCommands`, `GetCommand`, `RetryCommand`, `ClaimCommand`, `StartCommand`, `CompleteCommand` methods on `*postgres.Store`.

- [ ] **Step 1: Write failing owner persistence tests**

Use `sqlmock` to require one transaction for task plus all target rows:

```go
detail, err := store.CreateCommand(ctx, task, []string{"node-1", "node-2"})
if err != nil { t.Fatal(err) }
if len(detail.Executions) != 2 || detail.Executions[0].Status != core.CommandQueued {
    t.Fatalf("detail=%#v", detail)
}
```

The mock must expect a single query that validates every target is a Windows node before any insert, then `INSERT INTO command_tasks`, then one set-based `INSERT INTO command_executions ... SELECT unnest($2::text[])`.

- [ ] **Step 2: Run owner persistence tests and verify RED**

Run: `go test ./internal/postgres -run 'Test(Create|List|Get|Retry)Command' -count=1`

Expected: FAIL because Store methods and tables do not exist.

- [ ] **Step 3: Add idempotent schema and owner Store methods**

Create tables with explicit checks and indexes:

```sql
CREATE TABLE IF NOT EXISTS command_tasks (
    id TEXT PRIMARY KEY,
    shell TEXT NOT NULL CHECK (shell IN ('powershell', 'cmd')),
    command TEXT NOT NULL,
    timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 10 AND 1800),
    created_by TEXT NOT NULL REFERENCES owners(id) ON DELETE RESTRICT,
    retried_from_id TEXT REFERENCES command_tasks(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS command_executions (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES command_tasks(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    status TEXT NOT NULL CHECK (status IN ('queued','leased','running','succeeded','failed','timed_out')),
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    lease_token_hash TEXT,
    lease_expires_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    exit_code INTEGER,
    output TEXT NOT NULL DEFAULT '',
    output_truncated BOOLEAN NOT NULL DEFAULT FALSE,
    error_message TEXT NOT NULL DEFAULT '',
    duration_ms BIGINT,
    UNIQUE (task_id, node_id)
);
CREATE INDEX IF NOT EXISTS command_executions_claim_idx
    ON command_executions(node_id, status, task_id);
```

`CreateCommand` rejects an empty or duplicate Node ID array and rolls back unless all target nodes exist and have `type='windows'`. `ListCommands` limits results to 100 and uses conditional counts. `RetryCommand` inserts only source executions in `failed/timed_out` and returns `core.ErrConflict` when none qualify.

- [ ] **Step 4: Run owner persistence tests and verify GREEN**

Run: `go test ./internal/postgres -run 'Test(Create|List|Get|Retry)Command' -count=1`

Expected: PASS.

- [ ] **Step 5: Write failing lease transition tests**

Cover atomic ownership and status transitions:

```go
claim, found, err := store.ClaimCommand(ctx, "credential-hash", "lease-hash", now, 35*time.Minute)
if err != nil || !found { t.Fatalf("claim=%#v found=%v err=%v", claim, found, err) }
if claim.ExecutionID != "execution-1" || claim.LeaseExpiresAt != now.Add(35*time.Minute) {
    t.Fatalf("claim=%#v", claim)
}
```

Require `FOR UPDATE SKIP LOCKED`, device lookup through active `node_credentials`, expired `leased` recovery, expired `running` failure, and idempotent `start`/`complete` with the same hashes.

- [ ] **Step 6: Run lease tests and verify RED**

Run: `go test ./internal/postgres -run 'Test(Claim|Start|Complete)Command' -count=1`

Expected: FAIL because lease methods do not exist.

- [ ] **Step 7: Implement transactional leasing**

`ClaimCommand` opens a transaction, resolves the authenticated Node ID, updates expired states for that node, selects the earliest queued execution with `FOR UPDATE SKIP LOCKED`, writes `leased`, increments `attempt`, stores only `lease_token_hash`, commits, and returns `(zero,false,nil)` when no row exists.

`StartCommand` and `CompleteCommand` join `node_credentials` and compare both Credential hash and lease hash. Completion accepts only terminal states, sets bounded fields, and treats a repeated byte-identical terminal write as success; conflicting repeats return `core.ErrConflict`.

- [ ] **Step 8: Verify migration and Store suites**

Run: `go test ./internal/postgres -count=1`

Expected: PASS, including two consecutive `Migrate` calls.

- [ ] **Step 9: Commit new Store files; stage schema hunks only after reviewing existing edits**

```bash
git add internal/postgres/commands.go internal/postgres/commands_test.go internal/postgres/migration_integration_test.go
git diff --cached --check
git commit -m "feat: 持久化命令任务和执行租约"
```

Do not stage all of `schema.sql` until `git diff -- internal/postgres/schema.sql` confirms every staged line belongs to the combined ongoing Ace IT Center work.

### Task 3: Owner and Agent HTTP APIs

**Files:**
- Modify: `internal/api/repository.go`
- Modify: `internal/api/router.go`
- Modify: `internal/api/router_test.go`

**Interfaces:**
- Consumes: Task 2 Store methods.
- Produces: `/api/v1/commands*` and `/api/v1/agent/commands*` routes.
- Extends: `RouterOptions` with `CommandPollDuration` and `CommandPollInterval` for deterministic tests.

- [ ] **Step 1: Write failing Owner API tests**

```go
response := requestJSON(t, router, http.MethodPost, "/api/v1/commands", map[string]any{
    "node_ids": []string{"node-1"}, "shell": "powershell",
    "command": "Get-ComputerInfo", "timeout_seconds": 300,
}, &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})
if response.Code != http.StatusCreated { t.Fatalf("body=%s", response.Body.String()) }
```

Add separate tests for 401, empty command, invalid Shell, 9/1801 second bounds, duplicate IDs, Linux target, list/detail and retry.

- [ ] **Step 2: Run Owner API tests and verify RED**

Run: `go test ./internal/api -run 'TestOwner.*Command|TestCreateCommand' -count=1`

Expected: FAIL with missing routes/repository methods.

- [ ] **Step 3: Implement Owner routes and bounded request parsing**

Register authenticated routes and use `http.MaxBytesReader` with a 64 KiB request limit. Retrieve `core.Owner` from Gin context, generate UUIDs server-side, validate through `core.ValidateCommand`, and return safe 400/401/404/409/500 responses.

- [ ] **Step 4: Run Owner API tests and verify GREEN**

Run: `go test ./internal/api -run 'TestOwner.*Command|TestCreateCommand' -count=1`

Expected: PASS.

- [ ] **Step 5: Write failing Agent API tests**

Configure `CommandPollDuration: 3*time.Millisecond` and `CommandPollInterval: time.Millisecond`, then verify:

```go
request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/commands/claim", nil)
request.Header.Set("Authorization", "Bearer device-secret")
router.ServeHTTP(response, request)
if response.Code != http.StatusNoContent { t.Fatalf("body=%s", response.Body.String()) }
```

Also verify a successful claim returns the plain lease token once while the fake Repository receives only its hash; start/complete require both active device Credential and lease token; oversized output and non-terminal completion are rejected.

- [ ] **Step 6: Run Agent API tests and verify RED**

Run: `go test ./internal/api -run 'TestAgentCommand' -count=1`

Expected: FAIL because Agent routes do not exist.

- [ ] **Step 7: Implement long polling and lease endpoints**

Poll immediately, then on a ticker until `CommandPollDuration` elapses or request context ends. Generate a fresh opaque lease token before each Repository claim attempt, pass only `hashToken(token)`, and include the plain value only in a successful claim JSON. Use a 300 KiB completion request limit, 256 KiB output limit, 512-byte error limit, valid UTF-8 checks, and HTTP 204 when no work exists.

- [ ] **Step 8: Run the full API suite**

Run: `go test ./internal/api -count=1`

Expected: PASS without a 20-second test delay.

- [ ] **Step 9: Commit reviewed API hunks only**

```bash
git diff -- internal/api/repository.go internal/api/router.go internal/api/router_test.go
git add internal/api/repository.go internal/api/router.go internal/api/router_test.go
git diff --cached --check
git commit -m "feat: 提供命令中心服务端接口"
```

### Task 4: Agent HTTP Client and Command Loop

**Files:**
- Modify: `agent/internal/agent/client.go`
- Modify: `agent/internal/agent/client_test.go`
- Create: `agent/internal/app/command_worker.go`
- Create: `agent/internal/app/command_worker_test.go`

**Interfaces:**
- Produces: `Client.ClaimCommand`, `Client.StartCommand`, `Client.CompleteCommand`。
- Produces: `app.CommandClient`, `app.CommandExecutor`, `app.CommandWorker.Run`。

- [ ] **Step 1: Write failing Agent Client tests**

Use `httptest.Server` to assert exact paths, Bearer header and JSON:

```go
claim, found, err := client.ClaimCommand(ctx, "device-secret")
if err != nil || !found || claim.LeaseToken != "lease-secret" {
    t.Fatalf("claim=%#v found=%v err=%v", claim, found, err)
}
```

Add cases for HTTP 204, HTTP 409 mapped to exported `agent.ErrCommandLeaseRejected`, and completion payload serialization.

- [ ] **Step 2: Run Client tests and verify RED**

Run: `go test ./agent/internal/agent -run 'TestClient.*Command' -count=1`

Expected: FAIL because methods are missing.

- [ ] **Step 3: Implement Client methods**

Refactor response handling so `ClaimCommand` can distinguish 204 without attempting JSON decode. Never include Credential or lease token in returned error strings. Keep existing enrollment, pairing, heartbeat and log methods behavior unchanged.

- [ ] **Step 4: Run Client tests and verify GREEN**

Run: `go test ./agent/internal/agent -count=1`

Expected: PASS.

- [ ] **Step 5: Write failing command loop tests**

Use fakes that record call order:

```go
want := []string{"claim", "start", "execute", "complete"}
if !reflect.DeepEqual(calls, want) { t.Fatalf("calls=%v want=%v", calls, want) }
```

Avoid adding a comparison dependency. Cover no-task repoll, transient claim backoff, start failure without execution, terminal completion retry, context cancellation, and strictly serial execution.

- [ ] **Step 6: Run command loop tests and verify RED**

Run: `go test ./agent/internal/app -run 'TestCommandWorker' -count=1`

Expected: FAIL because `CommandWorker` is missing.

- [ ] **Step 7: Implement the command loop**

Expose:

```go
type CommandClient interface {
    ClaimCommand(context.Context, string) (core.CommandClaim, bool, error)
    StartCommand(context.Context, string, string, string) error
    CompleteCommand(context.Context, string, core.CommandCompletion) error
}

type CommandExecutor interface {
    Execute(context.Context, core.CommandClaim) core.CommandCompletion
}
```

`Run` uses one goroutine and one execution at a time. Transient claim errors wait 5 seconds with context-aware cancellation. Start and completion use capped exponential delays of 1, 2, 4, 8, 15 seconds until success, explicit lease rejection, context cancellation or `LeaseExpiresAt`.

- [ ] **Step 8: Run command loop tests and verify GREEN**

Run: `go test ./agent/internal/app -run 'TestCommandWorker' -count=1`

Expected: PASS.

- [ ] **Step 9: Commit new command loop files and reviewed Client hunks**

```bash
git add agent/internal/app/command_worker.go agent/internal/app/command_worker_test.go
git diff --cached --check
git commit -m "feat: 增加 Agent 命令领取循环"
```

### Task 5: Windows Command Executor and Service Integration

**Files:**
- Create: `agent/internal/command/executor.go`
- Create: `agent/internal/command/executor_test.go`
- Create: `agent/internal/command/process_windows.go`
- Create: `agent/internal/command/process_nonwindows.go`
- Modify: `agent/internal/app/worker.go`
- Modify: `agent/internal/app/worker_test.go`
- Modify: `agent/cmd/ace-agent/main.go`
- Modify: `agent/cmd/ace-agent/main_test.go`

**Interfaces:**
- Consumes: Task 4 `CommandExecutor`.
- Produces: `command.NewExecutor(Runner)`, `Executor.Execute`, `command.NewPlatformRunner() (Runner, bool)`。

- [ ] **Step 1: Write failing executor tests**

Create a fake `Runner` receiving the exact executable and arguments. Assert:

```go
if got.Program != "powershell.exe" { t.Fatalf("program=%q", got.Program) }
wantArgs := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "hostname"}
```

CMD must use `cmd.exe /D /S /C`. Separate tests assert exit 0 -> `succeeded`, exit 5 -> `failed`, deadline -> `timed_out`, invalid Shell never invokes Runner, invalid bytes become valid UTF-8, and output over 256 KiB is truncated while the Writer continues accepting bytes.

- [ ] **Step 2: Run executor tests and verify RED**

Run: `go test ./agent/internal/command -count=1`

Expected: FAIL because package does not exist.

- [ ] **Step 3: Implement bounded executor**

Define:

```go
type Invocation struct { Program string; Args []string }
type Runner interface { Run(context.Context, Invocation, io.Writer) (int, error) }
type Executor struct { runner Runner }
```

Use a mutex-protected limited Writer because stdout and stderr can write concurrently. `Execute` derives a timeout context from `claim.TimeoutSeconds`, records monotonic duration, normalizes UTF-8, limits error summaries to 512 bytes, and never includes Credential or lease token in arguments or errors.

- [ ] **Step 4: Run executor tests and verify GREEN**

Run: `go test ./agent/internal/command -count=1`

Expected: PASS on Linux with fake Runner.

- [ ] **Step 5: Implement and compile-check the Windows Runner**

`process_windows.go` starts `exec.Command` with a hidden window and `CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW`. It assigns the same Writer to stdout and stderr. On context cancellation it runs `taskkill.exe /PID <pid> /T /F`; if that fails, it calls `Process.Kill`, then always waits to release handles. `process_nonwindows.go` returns `(nil,false)` so Linux never claims Windows commands.

Run: `GOOS=windows GOARCH=amd64 go test -c -o /tmp/ace-command-windows.test.exe ./agent/internal/command`

Expected: exit 0 and a Windows PE test binary.

- [ ] **Step 6: Write failing Worker isolation test**

Inject a command loop that returns `errors.New("command channel unavailable")`; verify two or more heartbeats still occur and `StateError` is not published solely because the command loop failed.

Run: `go test ./agent/internal/app -run TestWorkerKeepsHeartbeatRunningWhenCommandLoopFails -count=1`

Expected: FAIL because Worker has no command loop dependency.

- [ ] **Step 7: Integrate command loop without coupling heartbeat state**

Add `CommandLoop func(context.Context, agent.Config) error` and `CommandErrorSink func(string)` to `app.Dependencies`. Start it once after configuration validation, wait for it during shutdown, sanitize errors, and keep heartbeat state independent.

In `serviceWorker.Run`, only inject the loop when `command.NewPlatformRunner()` returns supported. Use a 30-second HTTP Client timeout so a 20-second long poll has response margin. Log task ID, Node ID, status and duration only; never log command, output, Credential or lease token.

- [ ] **Step 8: Run Agent suites and Windows build gate**

Run: `go test ./agent/... -count=1`

Run: `GOOS=windows GOARCH=amd64 go test -c -o /tmp/ace-agent-windows.test.exe ./agent/cmd/ace-agent`

Expected: both commands exit 0.

- [ ] **Step 9: Commit new executor files; stage existing dirty files only after hunk review**

```bash
git add agent/internal/command/executor.go agent/internal/command/executor_test.go agent/internal/command/process_windows.go agent/internal/command/process_nonwindows.go
git diff --cached --check
git commit -m "feat: 执行 Windows 远程命令"
```

### Task 6: Web Command Center

**Files:**
- Modify: `frontend/src/types.ts`
- Create: `frontend/src/components/CommandCenter.vue`
- Create: `frontend/src/components/CommandCenter.test.ts`
- Modify: `frontend/src/components/OperationsWorkspace.vue`
- Modify: `frontend/src/components/OperationsWorkspace.test.ts`
- Modify: `frontend/src/style.css`

**Interfaces:**
- Consumes: Owner Command APIs from Task 3.
- Produces: `CommandCenter` component with `nodes: Node[]` prop and no global state dependency.

- [ ] **Step 1: Write failing Command Center component tests**

Mount with one Windows and one Linux Node. Assert only Windows is selectable, offline Windows remains selectable, submit sends exact JSON, and missing risk confirmation prevents the request:

```ts
expect(apiRequest).toHaveBeenCalledWith('/api/v1/commands', {
  method: 'POST',
  body: JSON.stringify({
    node_ids: ['node-1'], shell: 'powershell', command: 'hostname', timeout_seconds: 300,
  }),
})
```

Add tests for 5-second history polling, clearing the timer on unmount, status counts, pure-text output, detail loading and retry.

- [ ] **Step 2: Run component tests and verify RED**

Run: `npm test -- CommandCenter.test.ts`

Workdir: `frontend`

Expected: FAIL because component and types do not exist.

- [ ] **Step 3: Implement Command Center behavior**

Use native checkboxes for devices, a two-option segmented control for Shell, a numeric timeout input and a plain `<textarea>` command editor. Disable submit until at least one device, non-empty command and explicit LocalSystem risk confirmation exist. Fetch `GET /api/v1/commands` on mount and every 5 seconds; stop on unmount. Use an Element Plus drawer/dialog already available in the bundle for details. Render output with `<pre v-text="execution.output">`.

- [ ] **Step 4: Run component tests and verify GREEN**

Run: `npm test -- CommandCenter.test.ts`

Workdir: `frontend`

Expected: PASS.

- [ ] **Step 5: Write failing workspace navigation test**

Click `a[href="#commands"]`; assert `aria-current="page"`, title `命令中心`, `INFRASTRUCTURE / COMMANDS`, and that the component receives all Nodes. Navigate away and assert the Command Center unmounts so polling stops.

Run: `npm test -- OperationsWorkspace.test.ts`

Workdir: `frontend`

Expected: FAIL because the navigation item is missing.

- [ ] **Step 6: Add navigation and restrained responsive styling**

Import the Element Plus `Monitor` icon, extend `WorkspaceView` with `commands`, add page title and render `<CommandCenter :nodes="nodes" />`. Use full-width bands, stable table columns, 4 px radius, existing CSS variables, a minimum 44 px action height, a fixed-height scrollable output area and horizontal table scrolling below desktop width. Do not nest cards or add decorative gradients.

- [ ] **Step 7: Run frontend tests and production build**

Run: `npm test`

Run: `npm run build`

Workdir: `frontend`

Expected: all tests pass and Vue/TypeScript production build exits 0.

- [ ] **Step 8: Commit new frontend component; stage modified dirty files only after review**

```bash
git add frontend/src/components/CommandCenter.vue frontend/src/components/CommandCenter.test.ts
git diff --cached --check
git commit -m "feat: 增加网页命令中心"
```

### Task 7: Integrated Verification, V0.4.0 Build and DSM Deployment

**Files:**
- Modify: `frontend/src/components/OperationsWorkspace.vue` fallback version from `0.3.8` to `0.4.0`.
- Modify: `frontend/src/components/OperationsWorkspace.test.ts` expected fallback version.
- Modify: `deploy/README.md` with immutable release facts after deployment.
- Remote update: `/volume4/docker/docker/ace-it-center` through SSH and Compose.

**Interfaces:**
- Consumes: all prior tasks.
- Produces: stable `V0.4.0` installer, updated `latest.json`, healthy DSM project and public Command Center.

- [ ] **Step 1: Run fresh local verification**

Run: `go test ./... -count=1`

Run: `npm test`

Run: `npm run build`

Workdir for npm commands: `frontend`.

Run: `sh scripts/build-windows-agent.test.sh`

Run: `sh scripts/publish-windows-release.test.sh`

Expected: every command exits 0 with zero failed tests.

- [ ] **Step 2: Review the complete change set for secrets and unrelated edits**

Run: `git diff --check`

Run: `rg -n "51714769|Bearer [A-Za-z0-9_-]{16,}|lease-secret|device-secret" --glob '!**/*_test.go' --glob '!docs/superpowers/**' .`

Expected: no real credentials, lease values, trailing whitespace or conflict markers in production artifacts.

- [ ] **Step 3: Synchronize the reviewed repository to the DSM project directory**

Use SSH with the known HTTP ProxyCommand and copy only repository sources needed by Compose/build. Exclude `.git`, local `postgres`, local `releases`, `node_modules`, `frontend/dist`, `.env`, secrets and workflow scratch data. Before overwriting remote source, inspect remote `compose.yaml`, `.env`, running project labels and release directory to preserve persistent data.

- [ ] **Step 4: Rebuild backend/web through the DSM Compose project**

Remote workdir: `/volume4/docker/docker/ace-it-center`

Run: `docker compose config --quiet`

Run: `docker compose up -d --build postgres backend web`

Run: `docker compose ps`

Expected: project name `ace-it-center`; `postgres`, `backend` and `web` are healthy; no standalone replacement containers are created.

- [ ] **Step 5: Build and atomically publish Windows Agent V0.4.0**

Set remote environment values from the exact source revision and UTC clock used by the release:

```bash
export RELEASE_VERSION=0.4.0
export RELEASE_COMMIT="$(git rev-parse HEAD)"
export RELEASE_BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
export RELEASE_PUBLISHED_AT="$RELEASE_BUILT_AT"
```

Run the existing `deploy/windows-builder.compose.yaml` one-shot builder with `docker compose run --rm windows-builder`. Verify the builder container no longer exists after completion. Do not delete `/volume4/docker/docker/ace-it-center/releases` or the signing key.

- [ ] **Step 6: Verify public release and services**

Run public checks against `http://it.ace-station.top:1111`:

```text
GET /api/v1/health -> 200 and {"status":"ok"}
GET /downloads/windows/stable/latest.json -> version 0.4.0
GET /downloads/windows/stable/AceAgentSetup-windows-amd64-V0.4.0.exe -> 200
```

Download the installer, compare exact `size` and SHA-256 with `latest.json`, and verify the webpage sidebar shows `V0.4.0` and the download action uses the versioned filename.

- [ ] **Step 7: Perform real Windows command acceptance**

After the user installs `V0.4.0` on one Windows device, execute these controlled commands from the Web:

```powershell
Write-Output "ace-command-ok"
```

```cmd
cmd /D /S /C "echo ace-command-failed & exit /b 7"
```

```powershell
Start-Sleep -Seconds 20
```

Set the last task timeout to 10 seconds. Verify success output/exit 0, failure/exit 7, timeout/process termination, offline queue recovery, persistence after Web refresh, and heartbeats during an active command.

- [ ] **Step 8: Record immutable release evidence**

Append to `deploy/README.md`: UTC deployment time, git commit, backend/web image IDs, installer filename, size, SHA-256, manifest version, Compose health state, automated test counts, real Windows acceptance results and any remaining unverified item. Do not record passwords, cookies, Credentials or lease tokens.

- [ ] **Step 9: Final staged-file audit and commit**

Run: `git status --short`

Run: `git diff --cached --name-only`

Run: `git diff --cached --check`

Commit only files reviewed as part of this Command Center release, using:

```bash
git commit -m "feat: 发布 Windows 命令中心 V0.4.0"
```

Do not stage unrelated user work merely to obtain a clean status.
