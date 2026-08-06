# Ace IT Center Web-Managed Upgrades Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 Ace IT Center 交付仅 Owner 可操作的 Web 手动升级能力，安全升级 backend/web、失败自动回滚，并在成功后精准删除本次被替换的旧镜像。

**Architecture:** 独立 `ace-updater` 容器通过固定、可测试的 Docker Compose CLI 适配器管理 backend/web，持久化升级任务并用目标 digest 与本地回滚别名消除可变标签风险。Backend 只通过带内部凭据的 HTTP Client 代理 Owner 请求，不接触 Docker socket；Vue 页面检查版本、确认升级并在服务重启期间持续恢复任务状态。

**Tech Stack:** Go 1.26、Gin、`github.com/google/go-containerregistry`、Docker CLI/Compose v2、PostgreSQL 16 `pg_dump`、Vue 3、TypeScript、Element Plus、Vitest、GitHub Actions、GHCR、DSM 7.2 Container Manager。

## Global Constraints

- GitHub 源码仓库保持 Private；backend、web、updater 三个 GHCR Package 必须设为 Public。
- backend/web 正式版本同时发布不可变 `vX.Y.Z` 标签，并在全部镜像发布成功后依次移动各自 `stable` 标签。
- updater 只使用不可变版本标签，由 DSM 手动更新；Web 升级不得替换 postgres 或 updater。
- 只有 updater 挂载 `/var/run/docker.sock`；backend 和 web 不得挂载 Docker socket。
- updater 不开放宿主机端口，只允许管理 Compose project `ace-it-center` 的 `backend`、`web` 服务。
- 升级只接受当前检查结果中的目标版本，不接受任意镜像名、容器名、Compose 路径、Shell 命令或 Docker API 路径。
- backend/web 切换必须使用 `repo@sha256:digest` 任务级 override 和 `pull_policy: never`；回滚必须使用本地任务级别名和 `pull_policy: never`。
- 升级前必须成功生成 PostgreSQL custom-format 备份；备份失败不得切换容器。
- backend、web 和数据库迁移必须保持向后兼容；破坏性迁移版本不得进入 `stable`。
- 成功后只删除任务记录的旧 backend/web 别名和镜像 ID；禁止 `docker image prune`、模糊名称匹配和强制删除。
- 失败或回滚期间保留新旧镜像；回滚失败进入 `manual_intervention` 并阻止新任务。
- 浏览器与普通日志不得出现内部 Token、Cookie、数据库密码、Docker 环境、镜像内部错误或堆栈。
- 所有新行为先写失败测试，再写最小实现；每个任务只暂存列出的文件，禁止 `git add .`。

## File Structure

- Create `internal/systemupdate/types.go`: 服务名、阶段、镜像、检查结果、持久任务及脱敏 API View。
- Create `internal/systemupdate/types_test.go`: 枚举、SemVer、终态和敏感字段脱敏测试。
- Create `internal/systemupdate/store.go`: JSON 状态文件的原子读取与写入。
- Create `internal/systemupdate/store_test.go`: 权限、损坏文件、临时文件和原子替换测试。
- Create `internal/systemupdate/registry.go`: Public GHCR 匿名 manifest/config 解析。
- Create `internal/systemupdate/registry_test.go`: Registry 超时、OCI 标签和 digest 测试。
- Create `internal/systemupdate/checker.go`: 运行版本与 `stable` 版本的一致性、升级顺序检查。
- Create `internal/systemupdate/checker_test.go`: 版本不一致、非法 SemVer、降级和 digest 检查。
- Create `internal/systemupdate/runner.go`: 固定命令执行接口和受限错误输出。
- Create `internal/systemupdate/platform.go`: Docker/Compose、备份、健康检查、切换与精准清理适配器。
- Create `internal/systemupdate/platform_test.go`: 命令白名单、标签边界、override、备份和清理测试。
- Create `internal/systemupdate/manager.go`: 单任务锁、状态机、升级事务、回滚与重启恢复。
- Create `internal/systemupdate/manager_test.go`: 成功、各阶段失败、回滚失败、清理待处理和恢复测试。
- Create `internal/systemupdate/http.go`: updater 内部 API、恒定时间 Token 鉴权和安全错误映射。
- Create `internal/systemupdate/http_test.go`: 认证、请求边界、并发冲突和响应脱敏测试。
- Create `updater/cmd/ace-updater/config.go`: updater 环境变量加载与固定值校验。
- Create `updater/cmd/ace-updater/config_test.go`: 缺失 Token、路径、仓库和监听地址测试。
- Create `updater/cmd/ace-updater/main.go`: 依赖装配、启动恢复、HTTP Server 和优雅关闭。
- Create `updater/cmd/ace-updater/main_test.go`: 配置错误与依赖装配测试。
- Create `internal/updaterclient/client.go`: Backend 到 updater 的有界 HTTP Client。
- Create `internal/updaterclient/client_test.go`: Token、超时、状态码与原始错误隔离测试。
- Modify `internal/config/config.go`: Backend updater URL/Token 配置。
- Modify `internal/config/config_test.go`: 成对配置和 URL 校验测试。
- Create `internal/api/system_update.go`: Owner 系统升级 handlers 与 updater 接口。
- Create `internal/api/system_update_test.go`: 401、检查、启动、冲突和 503 测试。
- Modify `internal/api/router.go`: 注入 updater Client、注册 Owner 路由并让健康检查验证数据库连接。
- Modify `internal/api/router_test.go`: 数据库不可用时健康检查返回 503 的测试。
- Modify `backend/cmd/server/main.go`: 创建 updater Client 并注入 Router。
- Modify `frontend/src/types.ts`: 系统升级 View 类型。
- Create `frontend/src/components/SystemUpdate.vue`: 版本、确认、进度、断线恢复和终态页面。
- Create `frontend/src/components/SystemUpdate.test.ts`: 检查、确认、轮询、重试和脱敏展示测试。
- Modify `frontend/src/components/OperationsWorkspace.vue`: 增加系统升级导航与视图。
- Modify `frontend/src/components/OperationsWorkspace.test.ts`: Owner 导航、标题和移动端关闭测试。
- Modify `frontend/src/App.vue`: 处理升级页 Session 过期并返回登录状态。
- Modify `frontend/src/App.test.ts`: 升级页 401 后停止轮询并显示登录页的测试。
- Modify `frontend/src/style.css`: 紧凑的版本、阶段、状态与移动端布局。
- Modify `go.mod`, `go.sum`: 固定 `go-containerregistry` 依赖。
- Modify `Dockerfile`: 构建 updater，并提供含 Docker Compose CLI 与 `pg_dump` 的最小运行镜像。
- Modify `compose.yaml`: 加入 updater、内部凭据、状态/备份卷和固定 `stable` 默认值。
- Modify `.env.example`: 加入 updater 镜像、不可变标签和随机内部凭据配置。
- Modify `.github/workflows/ci-images.yml`: 测试并发布三个镜像，正式 tag 成功后提升 backend/web `stable`。
- Create `scripts/system-update-compose.test.sh`: 校验 Compose 权限边界、卷、网络和镜像标签策略。
- Create `scripts/system-update-dsm-smoke.sh`: 经显式确认执行真实 DSM Web 升级验收并比较容器/镜像状态。
- Create `scripts/system-update-dsm-smoke.test.sh`: 使用 fake `curl`/`docker` 验证 smoke 脚本保护条件和断言。
- Modify `scripts/deploy-dsm.sh`: 初次/手动部署包含 updater，并验证四服务健康。
- Modify `README.md`, `deploy/README.md`: Public GHCR、首次部署、手动 updater 更新、Web 升级和故障恢复说明。

---

### Task 1: Update Contracts and Atomic State

**Files:**
- Create: `internal/systemupdate/types.go`
- Create: `internal/systemupdate/types_test.go`
- Create: `internal/systemupdate/store.go`
- Create: `internal/systemupdate/store_test.go`

**Interfaces:**
- Produces: `ServiceName`, `Stage`, `CleanupStatus`, `Image`, `ImagePair`, `CheckResult`, `Task`, `PersistentState`, `StatusView`, `TaskView`。
- Produces: `ValidateVersion(string) error`, `Stage.Terminal() bool`, `Task.View() TaskView`。
- Produces: `NewFileStore(path string) *FileStore`, `(*FileStore).Load() (PersistentState, error)`, `(*FileStore).Save(PersistentState) error`。

- [ ] **Step 1: Write failing contract and redaction tests**

```go
func TestTaskViewNeverExposesRuntimeIdentifiers(t *testing.T) {
    task := systemupdate.Task{
        ID: "task-1", Stage: systemupdate.StagePulling,
        Original: systemupdate.ImagePair{Backend: systemupdate.Image{ID: "sha256:old", Digest: "sha256:old-digest", RollbackAlias: "ace-rollback-backend:task-1"}},
        Target: systemupdate.ImagePair{Backend: systemupdate.Image{Version: "v0.4.1", Digest: "sha256:new-digest"}},
    }
    encoded, err := json.Marshal(task.View())
    if err != nil { t.Fatal(err) }
    for _, secret := range []string{"sha256:old", "sha256:old-digest", "sha256:new-digest", "ace-rollback"} {
        if bytes.Contains(encoded, []byte(secret)) { t.Fatalf("public view leaked %q: %s", secret, encoded) }
    }
}

func TestValidateVersionRequiresCanonicalSemver(t *testing.T) {
    if err := systemupdate.ValidateVersion("v0.4.1"); err != nil { t.Fatal(err) }
    for _, value := range []string{"0.4.1", "latest", "v0.4", "v0.4.1 evil"} {
        if systemupdate.ValidateVersion(value) == nil { t.Fatalf("accepted %q", value) }
    }
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/systemupdate -run 'Test(TaskView|ValidateVersion|Stage)' -count=1`

Expected: FAIL because the package and public contracts do not exist.

- [ ] **Step 3: Implement exact types and public view conversion**

```go
type ServiceName string
const (
    ServiceBackend ServiceName = "backend"
    ServiceWeb ServiceName = "web"
)

type Stage string
const (
    StageChecking Stage = "checking"
    StageBackingUp Stage = "backing_up"
    StagePulling Stage = "pulling"
    StageSwitchingBackend Stage = "switching_backend"
    StageCheckingBackend Stage = "checking_backend"
    StageSwitchingWeb Stage = "switching_web"
    StageCheckingWeb Stage = "checking_web"
    StageStabilizing Stage = "stabilizing"
    StageCleaning Stage = "cleaning"
    StageRollingBack Stage = "rolling_back"
    StageSucceeded Stage = "succeeded"
    StageFailed Stage = "failed"
    StageManualIntervention Stage = "manual_intervention"
)

type CleanupStatus string
const (
    CleanupNotRun CleanupStatus = "not_run"
    CleanupComplete CleanupStatus = "complete"
    CleanupPending CleanupStatus = "pending"
)

type Image struct {
    Repository string `json:"repository"`
    Version string `json:"version"`
    Digest string `json:"digest"`
    ID string `json:"id"`
    RollbackAlias string `json:"rollback_alias"`
    PublishedAt *time.Time `json:"published_at,omitempty"`
}
type ImagePair struct { Backend Image `json:"backend"`; Web Image `json:"web"` }
type CheckResult struct { Current ImagePair `json:"current"`; Target ImagePair `json:"target"`; Available bool `json:"available"`; CheckedAt time.Time `json:"checked_at"` }
type Task struct {
    ID string `json:"id"`; Original ImagePair `json:"original"`; Target ImagePair `json:"target"`
    Stage Stage `json:"stage"`; BackupPath string `json:"backup_path"`; CreatedAt time.Time `json:"created_at"`
    StartedAt *time.Time `json:"started_at,omitempty"`; FinishedAt *time.Time `json:"finished_at,omitempty"`
    RolledBack bool `json:"rolled_back"`; Cleanup CleanupStatus `json:"cleanup"`
    ErrorCode string `json:"error_code,omitempty"`; ErrorMessage string `json:"error_message,omitempty"`
}
type PersistentState struct { LastCheck *CheckResult `json:"last_check,omitempty"`; Task *Task `json:"task,omitempty"` }
type VersionPairView struct { Backend string `json:"backend"`; Web string `json:"web"` }
type TaskView struct {
    ID string `json:"id"`; From VersionPairView `json:"from"`; To VersionPairView `json:"to"`
    Stage Stage `json:"stage"`; CreatedAt time.Time `json:"created_at"`
    StartedAt *time.Time `json:"started_at,omitempty"`; FinishedAt *time.Time `json:"finished_at,omitempty"`
    RolledBack bool `json:"rolled_back"`; Cleanup CleanupStatus `json:"cleanup"`
    ErrorCode string `json:"error_code,omitempty"`; ErrorMessage string `json:"error_message,omitempty"`
}
type ReleaseView struct { VersionPairView; PublishedAt *time.Time `json:"published_at,omitempty"` }
type StatusView struct {
    Current VersionPairView `json:"current"`; Latest *ReleaseView `json:"latest,omitempty"`
    UpdateAvailable bool `json:"update_available"`; CheckedAt *time.Time `json:"checked_at,omitempty"`
    Task *TaskView `json:"task,omitempty"`
}
```

`TaskView` and `StatusView` contain only task ID、backend/web versions、latest release time、stage、timestamps、rollback/cleanup flags and bounded error code/message. `ReleaseView.PublishedAt` is the later of the two OCI image creation times. Public views must not define JSON fields for repository、digest、image ID、rollback alias、backup path or internal configuration.

- [ ] **Step 4: Write failing atomic state tests**

Cover a missing file as empty state, valid round trip, JSON corruption as an error, parent directory creation with mode `0700`, state file mode `0600`, temporary-file cleanup, and an injected rename failure preserving the last valid file.

```go
store := systemupdate.NewFileStore(filepath.Join(t.TempDir(), "nested", "state.json"))
want := systemupdate.PersistentState{Task: &systemupdate.Task{ID: "task-1", Stage: systemupdate.StagePulling}}
if err := store.Save(want); err != nil { t.Fatal(err) }
got, err := store.Load()
if err != nil || !reflect.DeepEqual(got, want) { t.Fatalf("got=%#v err=%v", got, err) }
```

- [ ] **Step 5: Implement atomic state persistence**

Write JSON to a same-directory temporary file with mode `0600`, call `Sync`, close it, `Rename` it over the state file, then sync the parent directory. Reject files above `1 MiB` and use `json.Decoder.DisallowUnknownFields()` so a newer or corrupted schema does not silently lose rollback data.

- [ ] **Step 6: Verify package tests and commit**

Run: `go test ./internal/systemupdate -count=1`

Expected: PASS.

```bash
git add internal/systemupdate/types.go internal/systemupdate/types_test.go internal/systemupdate/store.go internal/systemupdate/store_test.go
git diff --cached --check
git commit -m "feat: 定义系统升级状态模型"
```

### Task 2: Public GHCR Discovery and Version Checking

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/systemupdate/registry.go`
- Create: `internal/systemupdate/registry_test.go`
- Create: `internal/systemupdate/checker.go`
- Create: `internal/systemupdate/checker_test.go`

**Interfaces:**
- Consumes: Task 1 `Image`, `ImagePair`, `CheckResult`, `ValidateVersion`。
- Produces: `ImageResolver.Resolve(ctx context.Context, repository, tag string) (Image, error)`。
- Produces: `RunningImageReader.InspectService(ctx context.Context, service ServiceName) (Image, error)`。
- Produces: `NewChecker(resolver ImageResolver, runtime RunningImageReader, backendRepo, webRepo string, now func() time.Time) *Checker` and `(*Checker).Check(context.Context) (CheckResult, error)`。

- [ ] **Step 1: Add the Registry dependency and failing resolver tests**

Run: `go get github.com/google/go-containerregistry@v0.20.3`

Use an `httptest.Server` implementing the Registry v2 Bearer challenge, token response, top-level manifest/index, linux/amd64 image manifest and config blob. Assert anonymous access, exact `stable` tag, top-level `sha256:` digest, and OCI labels:

```go
image, err := resolver.Resolve(ctx, serverRepository, "stable")
if err != nil { t.Fatal(err) }
if image.Version != "v0.4.1" || image.Digest != topLevelDigest {
    t.Fatalf("image=%#v", image)
}
```

- [ ] **Step 2: Run resolver tests and verify RED**

Run: `go test ./internal/systemupdate -run 'TestRegistry' -count=1`

Expected: FAIL because `RegistryResolver` does not exist.

- [ ] **Step 3: Implement anonymous, platform-specific GHCR resolution**

```go
type RegistryResolver struct {
    Transport http.RoundTripper
    Platform v1.Platform
}

func (r *RegistryResolver) Resolve(ctx context.Context, repository, tag string) (Image, error)
```

Parse the repository with `name.NewRepository(..., name.StrictValidation)`, allow only tag `stable`, use `authn.Anonymous`, select `linux/amd64`, retain the top-level manifest digest for `docker pull repo@digest`, and read:

- `org.opencontainers.image.version` as canonical `vX.Y.Z`;
- `org.opencontainers.image.created` as RFC3339 published time.

Limit each request with caller context, return typed safe errors, and never include response bodies or Registry auth challenges in public errors.

- [ ] **Step 4: Write failing checker tests**

Use fakes for `ImageResolver` and `RunningImageReader`. Cover matching upgrade, no update, mismatched current versions, mismatched `stable` versions, invalid SemVer, downgrade, missing digest, unexpected repository and partial Registry failure.

```go
result, err := checker.Check(context.Background())
if err != nil { t.Fatal(err) }
if !result.Available || result.Target.Backend.Version != "v0.4.1" || result.Target.Web.Version != "v0.4.1" {
    t.Fatalf("result=%#v", result)
}
```

- [ ] **Step 5: Implement strict version checking**

`Checker.Check` must inspect current backend/web first, resolve both fixed repositories at `stable`, require each pair to use one identical valid SemVer, require both target versions to be greater than both current versions, and stamp `CheckedAt` in UTC. It must return a configuration error when current versions differ and a retryable Registry error when either remote lookup fails.

- [ ] **Step 6: Verify and commit**

Run: `go test ./internal/systemupdate -run 'Test(Registry|Checker)' -count=1`

Expected: PASS.

```bash
git add go.mod go.sum internal/systemupdate/registry.go internal/systemupdate/registry_test.go internal/systemupdate/checker.go internal/systemupdate/checker_test.go
git diff --cached --check
git commit -m "feat: 检查 GHCR 稳定版本"
```

### Task 3: Restricted Docker Compose Platform Adapter

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/systemupdate/runner.go`
- Create: `internal/systemupdate/platform.go`
- Create: `internal/systemupdate/platform_test.go`

**Interfaces:**
- Consumes: Task 1 service/image/task types and Task 2 `RunningImageReader`。
- Produces: `CommandRunner.Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error)`。
- Produces: `Platform` methods used by Manager:

```go
type Platform interface {
    RunningImageReader
    CreateRollbackAlias(context.Context, ServiceName, Image, string) (Image, error)
    BackupDatabase(context.Context, string) (string, error)
    PullTarget(context.Context, ServiceName, Image) error
    DeployTarget(context.Context, ServiceName, ImagePair, string) error
    DeployRollback(context.Context, ServiceName, ImagePair, string) error
    WaitHealthy(context.Context, ServiceName) error
    RemoveOldImage(context.Context, ServiceName, Image) error
}
```

- [ ] **Step 1: Write failing command-boundary tests**

Build a fake `CommandRunner` that records every executable, argument and environment key. Assert:

- service values outside `backend|web` fail before invoking Docker;
- container discovery uses exact Compose project/service label filters and requires exactly one result;
- image IDs and digests match `sha256:[0-9a-f]{64}`;
- repositories equal the configured backend/web allowlist;
- no method accepts a caller-provided command, project name, Compose path or repository.

```go
_, err := platform.InspectService(ctx, systemupdate.ServiceName("postgres"))
if err == nil || len(runner.Calls) != 0 { t.Fatalf("err=%v calls=%#v", err, runner.Calls) }
```

- [ ] **Step 2: Run adapter tests and verify RED**

Run: `go test ./internal/systemupdate -run 'TestPlatform' -count=1`

Expected: FAIL because the platform adapter is absent.

- [ ] **Step 3: Implement inspected image and rollback alias operations**

```go
type PlatformConfig struct {
    ProjectName string
    ComposeFile string
    ComposeEnvFile string
    StateDir string
    BackupDir string
    BackendRepository string
    WebRepository string
    BackendHealthURL string
    WebHealthURL string
    HealthTimeout time.Duration
    HTTPClient *http.Client
    PGHost string
    PGPort string
    PGDatabase string
    PGUser string
    PGPassword string
}

func NewCLIPlatform(config PlatformConfig, runner CommandRunner) (*CLIPlatform, error)
```

Hard-require `ProjectName == "ace-it-center"`, absolute Compose/state/backup paths, fixed repositories, `http://backend:8080/api/v1/health`, and `http://web/api/v1/health`. Discover containers by both Compose labels, parse `docker inspect` JSON, then inspect the image ID for OCI version and `RepoDigests`.

Rollback aliases use `ace-it-center-rollback-{backend|web}:<lowercase-task-uuid>` and are created only from the recorded old image ID.

- [ ] **Step 4: Add failing backup, override, health and cleanup tests**

Assert that:

- `pg_dump --format=custom --file <temp>` receives password only through environment, never argv;
- a zero-byte or failed dump is rejected and not renamed to the final backup;
- target override contains fixed `repo@sha256:digest` plus `pull_policy: never`;
- rollback override contains only task rollback aliases plus `pull_policy: never`;
- Compose invocation is exactly `docker compose --project-name ace-it-center --env-file ... -f ... -f ... up -d --no-deps --force-recreate <service>`;
- health requires Docker `healthy` and HTTP 200;
- cleanup removes only allowlisted repo tags and the recorded rollback alias, refuses referenced images, and never passes `--force` or `image prune`.

- [ ] **Step 5: Implement fixed upgrade operations**

Write overrides under `<StateDir>/overrides/<task-id>-target.yaml` and `...-rollback.yaml` with mode `0600`; marshal fixed Go structs with the existing `github.com/goccy/go-yaml` module rather than formatting caller text. `PullTarget` runs `docker pull repo@digest`, re-inspects the local image, and rejects version/digest/repository mismatch before any Compose switch.

`BackupDatabase` writes `<BackupDir>/upgrade-<UTC timestamp>-<task-id>.dump` through a temporary file and atomic rename. `RemoveOldImage` first checks `docker ps -aq --filter ancestor=<id>`, removes only exact recorded aliases, then removes the exact image ID without force; any remaining reference/tag returns a cleanup-pending error.

- [ ] **Step 6: Verify and commit**

Run: `go test ./internal/systemupdate -run 'TestPlatform' -count=1`

Expected: PASS.

```bash
git add go.mod go.sum internal/systemupdate/runner.go internal/systemupdate/platform.go internal/systemupdate/platform_test.go
git diff --cached --check
git commit -m "feat: 限定 Docker 升级操作边界"
```

### Task 4: Successful Upgrade State Machine

**Files:**
- Create: `internal/systemupdate/manager.go`
- Create: `internal/systemupdate/manager_test.go`

**Interfaces:**
- Consumes: `Checker`, `FileStore`, `Platform`。
- Produces: `NewManager(ManagerOptions) (*Manager, error)`。
- Produces: `(*Manager).Status() StatusView`, `(*Manager).Check(context.Context) (StatusView, error)`, `(*Manager).Start(context.Context, string) (TaskView, error)`。
- `ManagerOptions.Launch func(func())` runs `go job()` in production and runs synchronously in tests.

- [ ] **Step 1: Write failing check/start tests**

Cover forced check persistence, two-minute check expiry, exact target-version confirmation, no-update rejection, single active task, and a synchronous successful call order:

```text
inspect -> rollback aliases -> backup -> pull backend -> pull web
-> deploy backend -> health backend -> deploy web -> health web
-> stability checks -> clean backend -> clean web
```

```go
view, err := manager.Start(ctx, "v0.4.1")
if err != nil { t.Fatal(err) }
if view.Stage != systemupdate.StageSucceeded || view.Cleanup != systemupdate.CleanupComplete {
    t.Fatalf("view=%#v", view)
}
```

- [ ] **Step 2: Run Manager tests and verify RED**

Run: `go test ./internal/systemupdate -run 'TestManager(Check|Start|Successful)' -count=1`

Expected: FAIL because `Manager` does not exist.

- [ ] **Step 3: Implement the single-task transaction**

```go
type ManagerOptions struct {
    Store *FileStore
    Checker *Checker
    Platform Platform
    Now func() time.Time
    NewID func() string
    Launch func(func())
    RootContext context.Context
    CheckTTL time.Duration
    StableWindow time.Duration
    StableInterval time.Duration
}
```

Production values are `CheckTTL=2m`, `StableWindow=30s`, `StableInterval=2s`. `Start` locks, reloads state, validates the last check and target version, saves a `checking` task before launch, then returns the latest `TaskView`. The detached job uses `RootContext`, not the HTTP request context.

Before and after every external action, save the next stage atomically. After both service checks, continue checking backend, web and database-backed health throughout the 30-second stability window. On success, update `LastCheck.Current` to the target and preserve only public-safe cleanup results in `TaskView`.

- [ ] **Step 4: Add cleanup-pending coverage**

When either exact old image cannot be removed because it remains referenced, the deployment still ends at `succeeded`, sets `CleanupPending`, and exposes the fixed message `升级成功，旧镜像仍被引用，需在 DSM 中处理` without returning Docker output.

- [ ] **Step 5: Verify package tests and commit**

Run: `go test ./internal/systemupdate -count=1`

Expected: PASS.

```bash
git add internal/systemupdate/manager.go internal/systemupdate/manager_test.go
git diff --cached --check
git commit -m "feat: 编排系统升级成功流程"
```

### Task 5: Rollback and Restart Recovery

**Files:**
- Modify: `internal/systemupdate/manager.go`
- Modify: `internal/systemupdate/manager_test.go`

**Interfaces:**
- Extends: `(*Manager).Recover(context.Context) error`。
- Keeps: `Start` blocked while task stage is non-terminal or `manual_intervention`。

- [ ] **Step 1: Write a failing stage-by-stage failure matrix**

Use table tests for alias, backup, each pull, backend switch/health, web switch/health, stability and cleanup. Assert:

- failures before the first switch set `failed` without recreating containers or deleting images;
- failures at/after backend switch call rollback for backend and web with local aliases;
- rollback health success sets `failed` and `RolledBack=true`;
- rollback failure sets `manual_intervention`, retains all images and blocks `Start`; `Check` may refresh version information but cannot clear or replace the blocked task.

Only these bounded codes may enter public state:

```text
state_invalid, check_expired, backup_failed, pull_failed,
backend_switch_failed, backend_unhealthy, web_switch_failed,
web_unhealthy, stability_failed, rollback_failed, cleanup_pending,
updater_restarted
```

- [ ] **Step 2: Run rollback tests and verify RED**

Run: `go test ./internal/systemupdate -run 'TestManager.*(Failure|Rollback|Manual)' -count=1`

Expected: FAIL because failure paths do not yet perform the required rollback transitions.

- [ ] **Step 3: Implement conservative rollback**

Persist `rolling_back` before calling Docker. Always deploy rollback backend then rollback web, even when only backend was switched, so both services end on the recorded original pair. Require both Docker health and HTTP health. Never call cleanup in a failed transaction. Store detailed errors only in injected `slog.Logger`; redact Token, environment and runner output before logging.

- [ ] **Step 4: Write failing updater-restart recovery tests**

Cover these exact rules:

- terminal task: no action;
- `checking|backing_up|pulling`: inspect both running services; if originals still run, mark `failed/updater_restarted` without switching;
- `switching_backend` through `stabilizing`: inspect actual digests and conservatively roll back both services;
- `cleaning`: if both target digests are running and healthy, resume exact cleanup and succeed; otherwise roll back;
- missing original alias/digest or failed recovery health: enter `manual_intervention`.

- [ ] **Step 5: Implement `Recover` and verify**

`Recover` must run once before the updater HTTP listener starts. It reloads disk state and inspects actual containers before choosing a branch; it never repeats backup or pull blindly.

Run: `go test ./internal/systemupdate -run 'TestManager' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit rollback and recovery**

```bash
git add internal/systemupdate/manager.go internal/systemupdate/manager_test.go
git diff --cached --check
git commit -m "feat: 支持升级回滚和重启恢复"
```

### Task 6: Updater Internal API and Process

**Files:**
- Create: `internal/systemupdate/http.go`
- Create: `internal/systemupdate/http_test.go`
- Create: `updater/cmd/ace-updater/config.go`
- Create: `updater/cmd/ace-updater/config_test.go`
- Create: `updater/cmd/ace-updater/main.go`
- Create: `updater/cmd/ace-updater/main_test.go`

**Interfaces:**
- Consumes: Task 4/5 Manager methods。
- Produces internal routes: `GET /health`, `GET /internal/v1/update`, `POST /internal/v1/update/check`, `POST /internal/v1/update`。
- Produces `LoadUpdaterConfig() (UpdaterConfig, error)` and `run(context.Context, UpdaterConfig) error`。

- [ ] **Step 1: Write failing HTTP authentication and boundary tests**

```go
request := httptest.NewRequest(http.MethodGet, "/internal/v1/update", nil)
request.Header.Set("Authorization", "Bearer "+token)
response := httptest.NewRecorder()
handler.ServeHTTP(response, request)
if response.Code != http.StatusOK { t.Fatalf("status=%d body=%s", response.Code, response.Body.String()) }
```

Assert missing/wrong tokens return 401 with the same body, comparison uses SHA-256 plus `subtle.ConstantTimeCompare`, `/health` contains only `{"status":"ok"}`, start bodies above `1 KiB` fail, unknown JSON fields fail, and no response contains digest、image ID、alias、path、Token or raw fake error.

- [ ] **Step 2: Run HTTP tests and verify RED**

Run: `go test ./internal/systemupdate -run 'TestHTTP' -count=1`

Expected: FAIL because the handler is absent.

- [ ] **Step 3: Implement the fixed internal API**

```go
type UpdateManager interface {
    Status() StatusView
    Check(context.Context) (StatusView, error)
    Start(context.Context, string) (TaskView, error)
}

type StartRequest struct { TargetVersion string `json:"target_version"` }
```

Return 202 for an accepted start, 409 for active/manual-intervention/check mismatch, 400 for invalid body/version, and 503 for retryable Registry/platform errors. The internal handler accepts no image, service, path or command field because `DisallowUnknownFields` rejects them.

- [ ] **Step 4: Write failing updater configuration tests**

Require and validate these values:

```text
ACE_UPDATER_TOKEN          at least 32 random characters and not replace-with-*
ACE_UPDATER_LISTEN_ADDR    default :8090
ACE_COMPOSE_PROJECT        exactly ace-it-center
ACE_COMPOSE_FILE           absolute existing regular file
ACE_COMPOSE_ENV_FILE       absolute existing regular file
ACE_UPDATER_STATE_FILE     absolute path under /state
ACE_UPDATER_BACKUP_DIR     absolute path under /backups
ACE_BACKEND_IMAGE          exactly configured GHCR backend repository
ACE_WEB_IMAGE              exactly configured GHCR web repository
PGHOST/PGPORT/PGDATABASE/PGUSER/PGPASSWORD required
```

- [ ] **Step 5: Implement process wiring and recovery-before-listen**

`run` creates the Registry resolver, CLI platform, state store, checker and manager; calls `manager.Recover` before binding the port; then starts an `http.Server` with 5-second header/read timeouts and 30-second write timeout. SIGINT/SIGTERM cancel the manager root context and allow 10 seconds for shutdown. Logs include task ID/stage/version only.

- [ ] **Step 6: Verify and commit**

Run: `go test ./internal/systemupdate ./updater/cmd/ace-updater -count=1`

Expected: PASS.

```bash
git add internal/systemupdate/http.go internal/systemupdate/http_test.go updater/cmd/ace-updater/config.go updater/cmd/ace-updater/config_test.go updater/cmd/ace-updater/main.go updater/cmd/ace-updater/main_test.go
git diff --cached --check
git commit -m "feat: 提供内部升级服务"
```

### Task 7: Owner-Authenticated Backend Proxy

**Files:**
- Create: `internal/updaterclient/client.go`
- Create: `internal/updaterclient/client_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Create: `internal/api/system_update.go`
- Create: `internal/api/system_update_test.go`
- Modify: `internal/api/router.go`
- Modify: `internal/api/router_test.go`
- Modify: `backend/cmd/server/main.go`

**Interfaces:**
- Consumes updater internal API from Task 6。
- Produces Owner routes: `GET /api/v1/system/update`, `POST /api/v1/system/update/check`, `POST /api/v1/system/update`。
- Extends `RouterOptions` with `SystemUpdater SystemUpdater`。

- [ ] **Step 1: Write failing updater Client tests**

```go
client, err := updaterclient.New(server.URL, token, server.Client())
if err != nil { t.Fatal(err) }
status, err := client.Check(context.Background())
if err != nil || status.Latest.Backend != "v0.4.1" { t.Fatalf("status=%#v err=%v", status, err) }
```

Assert a 5-second request timeout, exact Bearer header, same-origin-independent fixed base URL, `1 MiB` maximum response, unknown-field rejection, safe typed errors for 400/401/409/503, and no raw response body in `Error()`.

- [ ] **Step 2: Run Client tests and verify RED**

Run: `go test ./internal/updaterclient -count=1`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement the bounded internal Client**

```go
type Client struct { baseURL *url.URL; token string; httpClient *http.Client }
func New(rawURL, token string, httpClient *http.Client) (*Client, error)
func (c *Client) Status(context.Context) (systemupdate.StatusView, error)
func (c *Client) Check(context.Context) (systemupdate.StatusView, error)
func (c *Client) Start(context.Context, string) (systemupdate.TaskView, error)
```

Accept only `http` URL with no userinfo/query/fragment. The DSM value is `http://updater:8090`; never derive it from browser input or request headers.

- [ ] **Step 4: Add Backend config and API failure tests**

`internal/config.Load` must accept both updater fields absent for local development, reject only-one-present, validate URL, and reject short/placeholder tokens. A nil `SystemUpdater` keeps routes registered but returns a safe 503 after Owner authentication.

API tests must prove all three routes return 401 without a valid session, forward exact target version, reject unknown request fields, map updater conflict to 409, and never expose internal errors. Add a repository failure case proving `/api/v1/health` returns 503 with `{"status":"unavailable"}` when its lightweight `IsSetup` database query fails, while both initialized and not-yet-initialized reachable databases return 200.

- [ ] **Step 5: Implement Router injection and handlers**

```go
type SystemUpdater interface {
    Status(context.Context) (systemupdate.StatusView, error)
    Check(context.Context) (systemupdate.StatusView, error)
    Start(context.Context, string) (systemupdate.TaskView, error)
}
```

Register all routes under the existing `authenticated` group. Keep implementations in `system_update.go` so the existing large `router.go` only gains the option field, server field, three route declarations and database-backed health handler. `backend/cmd/server/main.go` constructs a Client only when both config values exist. This makes updater checks through backend and web also verify the PostgreSQL connection.

- [ ] **Step 6: Verify and commit**

Run: `go test ./internal/updaterclient ./internal/config ./internal/api ./backend/cmd/server -count=1`

Expected: PASS.

```bash
git add internal/updaterclient/client.go internal/updaterclient/client_test.go internal/config/config.go internal/config/config_test.go internal/api/system_update.go internal/api/system_update_test.go internal/api/router.go internal/api/router_test.go backend/cmd/server/main.go
git diff --cached --check
git commit -m "feat: 代理 Owner 系统升级请求"
```

### Task 8: System Update Web Experience

**Files:**
- Modify: `frontend/src/types.ts`
- Create: `frontend/src/components/SystemUpdate.vue`
- Create: `frontend/src/components/SystemUpdate.test.ts`
- Modify: `frontend/src/components/OperationsWorkspace.vue`
- Modify: `frontend/src/components/OperationsWorkspace.test.ts`
- Modify: `frontend/src/App.vue`
- Modify: `frontend/src/App.test.ts`
- Modify: `frontend/src/style.css`

**Interfaces:**
- Consumes Owner routes from Task 7。
- Produces `SystemUpdateStatus`, `SystemUpdateTask`, `SystemUpdateStage`, `CleanupStatus` TypeScript types。
- Adds workspace view `updates` and sidebar entry `#updates`。

- [ ] **Step 1: Write failing component behavior tests**

Mock `apiRequest` and cover:

- mount automatically calls `POST /api/v1/system/update/check`;
- equal current/latest versions disable “立即升级”;
- newer matching version enables it;
- latest release time uses the public `published_at` value and formats it in `zh-CN` without exposing image metadata;
- confirmation dialog shows the exact from/to version and submits once;
- active stages poll `GET /api/v1/system/update` every 2 seconds;
- 401 is surfaced to App through a dedicated `session-expired` emit;
- temporary 502/503 keeps the last known stage and continues polling;
- `succeeded + cleanup_pending` shows the DSM cleanup message;
- `failed` and `manual_intervention` show fixed safe actions;
- unmount clears timers.

```ts
expect(apiRequest).toHaveBeenCalledWith('/api/v1/system/update/check', { method: 'POST', body: '{}' })
await wrapper.get('[data-action="start-update"]').trigger('click')
expect(wrapper.get('[role="dialog"]').text()).toContain('v0.4.0')
expect(wrapper.get('[role="dialog"]').text()).toContain('v0.4.1')
```

- [ ] **Step 2: Run component tests and verify RED**

Run: `cd frontend && npm test -- --run src/components/SystemUpdate.test.ts`

Expected: FAIL because the component and types do not exist.

- [ ] **Step 3: Implement typed status rendering and resilient polling**

```ts
export type SystemUpdateStage =
  | 'checking' | 'backing_up' | 'pulling' | 'switching_backend' | 'checking_backend'
  | 'switching_web' | 'checking_web' | 'stabilizing' | 'cleaning'
  | 'rolling_back' | 'succeeded' | 'failed' | 'manual_intervention'

export interface SystemUpdateTask {
  id: string
  from: { backend: string; web: string }
  to: { backend: string; web: string }
  stage: SystemUpdateStage
  created_at: string
  started_at?: string
  finished_at?: string
  rolled_back: boolean
  cleanup: 'not_run' | 'complete' | 'pending'
  error_code?: string
  error_message?: string
}

export interface SystemUpdateStatus {
  current: { backend: string; web: string }
  latest?: { backend: string; web: string; published_at?: string }
  update_available: boolean
  checked_at?: string
  task?: SystemUpdateTask
}
```

Use Element Plus `ElDialog`, familiar refresh/update icons, a fixed-height stage list and compact version rows. Do not render raw HTML. Disable all mutation controls while a task is active or blocked. Poll only while the component is mounted and the task is non-terminal; backoff after disconnects but cap at 5 seconds so the page recovers promptly after backend/web restart.

- [ ] **Step 4: Add navigation and responsive tests**

Extend `WorkspaceView`, page title map and sidebar. Assert `#updates` selects “系统升级”, closes mobile navigation, renders `SystemUpdate`, and emits session expiry upward. `App.vue` handles that event by clearing `owner`, setting `phase='login'`, and stopping existing node/pairing polling. The entry exists only inside the authenticated Owner workspace.

- [ ] **Step 5: Implement navigation and styles**

Use `UploadFilled` from Element Plus for the navigation icon. Keep card radius at or below 8px, use existing colors, and add `minmax(0, 1fr)`, overflow wrapping and mobile stacked controls so long versions/error text cannot overlap.

- [ ] **Step 6: Verify frontend and commit**

Run: `cd frontend && npm test -- --run`

Run: `cd frontend && npm run build`

Expected: all Vitest tests PASS and Vue/TypeScript production build succeeds.

```bash
git add frontend/src/types.ts frontend/src/components/SystemUpdate.vue frontend/src/components/SystemUpdate.test.ts frontend/src/components/OperationsWorkspace.vue frontend/src/components/OperationsWorkspace.test.ts frontend/src/App.vue frontend/src/App.test.ts frontend/src/style.css
git diff --cached --check
git commit -m "feat: 增加系统升级页面"
```

### Task 9: Updater Image, Compose, and Stable Release Pipeline

**Files:**
- Modify: `Dockerfile`
- Modify: `compose.yaml`
- Modify: `.env.example`
- Modify: `.github/workflows/ci-images.yml`
- Create: `scripts/system-update-compose.test.sh`
- Modify: `scripts/deploy-dsm.sh`

**Interfaces:**
- Produces image `ghcr.io/s450586793/ace-it-center-updater:vX.Y.Z`。
- Changes backend/web defaults to `stable`; updater requires `ACE_UPDATER_IMAGE_TAG` immutable SemVer。
- Adds internal service `updater:8090` without a host `ports` mapping。

- [ ] **Step 1: Write failing Compose contract test**

`scripts/system-update-compose.test.sh` must render `docker compose config --format json` with a temporary env and use `jq -e` to assert:

```text
services = postgres, backend, web, updater
backend/web image tag = stable
updater image tag = v0.4.1 test fixture
only updater has /var/run/docker.sock
updater has no published ports
compose.yaml and .env mounts are read-only
state and backup mounts are writable
backend has ACE_UPDATER_URL and ACE_UPDATER_TOKEN
updater uses the same token and fixed project/repositories
postgres has no updater dependency and no recreated volume definition
```

- [ ] **Step 2: Run contract test and verify RED**

Run: `bash scripts/system-update-compose.test.sh`

Expected: FAIL because updater service and env keys are absent.

- [ ] **Step 3: Build the updater image target**

Add `updater-builder` that compiles `./updater/cmd/ace-updater`. Add runtime target `updater` based on Alpine 3.22 with only CA certificates、tzdata、Docker CLI、Docker Compose v2 plugin and PostgreSQL 16 client. Copy the static binary, keep no source/compiler, use `cap_drop: [ALL]` and `security_opt: [no-new-privileges:true]` in Compose. The Docker socket remains the documented high-privilege boundary.

- [ ] **Step 4: Add exact Compose configuration**

The root `compose.yaml` must include:

```yaml
  updater:
    image: ${ACE_UPDATER_IMAGE:-ghcr.io/s450586793/ace-it-center-updater}:${ACE_UPDATER_IMAGE_TAG:?ACE_UPDATER_IMAGE_TAG is required}
    pull_policy: always
    restart: unless-stopped
    environment:
      ACE_UPDATER_TOKEN: ${ACE_UPDATER_TOKEN:?ACE_UPDATER_TOKEN is required}
      ACE_COMPOSE_PROJECT: ace-it-center
      ACE_COMPOSE_FILE: /config/compose.yaml
      ACE_COMPOSE_ENV_FILE: /config/.env
      ACE_UPDATER_STATE_FILE: /state/update-state.json
      ACE_UPDATER_BACKUP_DIR: /backups
      ACE_BACKEND_IMAGE: ${ACE_BACKEND_IMAGE:-ghcr.io/s450586793/ace-it-center-backend}
      ACE_WEB_IMAGE: ${ACE_WEB_IMAGE:-ghcr.io/s450586793/ace-it-center-web}
      PGHOST: postgres
      PGPORT: "5432"
      PGDATABASE: ${POSTGRES_DB:-ace_it_center}
      PGUSER: ${POSTGRES_USER:-ace}
      PGPASSWORD: ${POSTGRES_PASSWORD}
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ${ACE_DATA_DIR:-.}/compose.yaml:/config/compose.yaml:ro
      - ${ACE_DATA_DIR:-.}/.env:/config/.env:ro
      - ${ACE_DATA_DIR:-.}/updater-state:/state
      - ${ACE_DATA_DIR:-.}/backups:/backups
```

Add an internal healthcheck, `depends_on: postgres: condition: service_healthy`, and only the `internal` network. Backend receives `ACE_UPDATER_URL=http://updater:8090` and the same Token. `.env.example` sets `ACE_IMAGE_TAG=stable`, declares the updater repository, and uses invalid placeholders that startup validation rejects until the operator sets an immutable updater tag and random Token.

- [ ] **Step 5: Rewrite release tagging around immutable publication**

The publish matrix contains backend、web、updater and pushes `latest`/`sha-*` on `main`, plus immutable `vX.Y.Z` on valid release tags. A separate `promote-stable` job runs only after the complete matrix succeeds and executes:

```bash
docker buildx imagetools create \
  --tag ghcr.io/${GITHUB_REPOSITORY_OWNER}/ace-it-center-backend:stable \
  ghcr.io/${GITHUB_REPOSITORY_OWNER}/ace-it-center-backend:${GITHUB_REF_NAME}
docker buildx imagetools create \
  --tag ghcr.io/${GITHUB_REPOSITORY_OWNER}/ace-it-center-web:stable \
  ghcr.io/${GITHUB_REPOSITORY_OWNER}/ace-it-center-web:${GITHUB_REF_NAME}
```

Do not promote updater to `stable`. Add the Compose contract test to CI before publish. Retain OCI labels from `docker/metadata-action`, including version、revision and created time.

- [ ] **Step 6: Update manual deployment and verify**

`scripts/deploy-dsm.sh` validates Compose, pulls backend/web/updater, starts the project, checks public web health plus updater container health, and leaves updater version changes under explicit `.env`/DSM control.

Run: `bash scripts/system-update-compose.test.sh`

Run: `docker build --target backend -t ace-backend-plan-test .`

Run: `docker build --target web -t ace-web-plan-test .`

Run: `docker build --target updater -t ace-updater-plan-test .`

Expected: contract PASS and all three image targets build.

- [ ] **Step 7: Commit container and release changes**

```bash
git add Dockerfile compose.yaml .env.example .github/workflows/ci-images.yml scripts/system-update-compose.test.sh scripts/deploy-dsm.sh
git diff --cached --check
git commit -m "feat: 发布并部署独立升级服务"
```

### Task 10: DSM Upgrade Smoke Test, Documentation, and Full Verification

**Files:**
- Create: `scripts/system-update-dsm-smoke.sh`
- Create: `scripts/system-update-dsm-smoke.test.sh`
- Modify: `README.md`
- Modify: `deploy/README.md`

**Interfaces:**
- Produces guarded production smoke command using `ACE_CONFIRM_SYSTEM_UPDATE=yes`。
- Documents first Public GHCR setup, immutable updater updates, Web upgrade, backups, cleanup-pending and manual-intervention recovery。

- [ ] **Step 1: Write failing smoke-script contract tests**

Fake `curl`, `docker` and `jq` responses and assert the script:

- exits before login or Docker calls unless `ACE_CONFIRM_SYSTEM_UPDATE=yes`;
- requires URL、Owner username/password and expected target version through environment;
- uses a mode-`0600` temporary cookie jar and deletes it on exit;
- records postgres/updater container ID、image ID and `StartedAt` before upgrade;
- logs in, forces check, requires the exact expected target, starts once, and polls boundedly;
- fails on `failed|manual_intervention`;
- verifies backend/web target versions and health;
- verifies postgres/updater IDs and `StartedAt` are unchanged;
- verifies the PostgreSQL backup exists;
- verifies cleanup result or prints the exact cleanup-pending instruction;
- never prints password、Cookie、Token or raw API body.

- [ ] **Step 2: Run smoke contract and verify RED**

Run: `bash scripts/system-update-dsm-smoke.test.sh`

Expected: FAIL because the guarded smoke script does not exist.

- [ ] **Step 3: Implement the guarded DSM smoke script**

Use `set -euo pipefail`, `mktemp`, trap cleanup, `curl --fail --silent --show-error --max-time`, and `jq -e`. Cap polling at 10 minutes with 2-second intervals. The script performs the intended production mutation only after the explicit confirmation variable and exact expected-version comparison both pass.

- [ ] **Step 4: Update operator documentation**

Document exact one-time and recurring operations:

1. Source repository remains Private.
2. After first package publication, set backend、web、updater GHCR visibility to Public in GitHub Package settings; DSM does not run `docker login`.
3. Generate `ACE_UPDATER_TOKEN` with at least 32 random bytes, store it only in DSM `.env`, set mode `0600`.
4. Set `ACE_UPDATER_IMAGE_TAG` to an immutable `vX.Y.Z` and start the four-service Compose project.
5. Use the Owner “系统升级” page for backend/web stable upgrades.
6. Update updater itself only through DSM Container Manager or explicit `.env` tag change followed by manual Compose recreation.
7. Locate backups under `${ACE_DATA_DIR}/backups`.
8. For `cleanup_pending`, inspect references and remove only the displayed Ace IT Center old image after confirmation.
9. For `manual_intervention`, stop updater, preserve `updater-state/update-state.json` and all images, inspect backend/web, recover both to one version, then archive the blocked state file before restarting updater.
10. On the first rollout only, remove legacy `pre-v*` backend/web images after proving no container references them; do not include postgres、updater、Windows builder or other projects.

Remove the existing instruction that Private GHCR login is required. State clearly that Public images reveal filesystem layers and therefore must never contain secrets or business data.

- [ ] **Step 5: Run all automated verification**

Run: `go test ./... -count=1`

Run: `bash scripts/build-windows-agent.test.sh`

Run: `bash scripts/publish-windows-release.test.sh`

Run: `bash scripts/cleanup-dsm-images.test.sh`

Run: `bash scripts/system-update-compose.test.sh`

Run: `bash scripts/system-update-dsm-smoke.test.sh`

Run: `cd frontend && npm test -- --run`

Run: `cd frontend && npm run build`

Run: `docker compose --env-file .env.example config --quiet` only after substituting test-safe values for the intentionally invalid updater Token/tag placeholders in a temporary env file.

Expected: all Go、shell、Vitest、Vue typecheck/build and Compose checks PASS.

- [ ] **Step 6: Run DSM acceptance after a release is published**

Before running, capture all local image IDs. Publish a real `vX.Y.Z`, verify all three GHCR Packages are Public, manually deploy the matching updater immutable tag, then execute:

```bash
read -r -p 'Owner username: ' ACE_OWNER_USERNAME
read -r -s -p 'Owner password: ' ACE_OWNER_PASSWORD
read -r -p 'Expected release (for example v0.4.1): ' ACE_EXPECTED_TARGET
export ACE_OWNER_USERNAME ACE_OWNER_PASSWORD ACE_EXPECTED_TARGET
ACE_CONFIRM_SYSTEM_UPDATE=yes ACE_BASE_URL=http://127.0.0.1:9060 \
  bash scripts/system-update-dsm-smoke.sh
```

Repeat with controlled backend and web health failures to verify rollback, then compare all non-backend/web image IDs to the initial snapshot. Confirm PostgreSQL `StartedAt` and volume are unchanged, updater is unchanged, old backend/web images are deleted only after success, and failure runs retain both versions.

- [ ] **Step 7: Commit documentation and smoke validation**

```bash
git add scripts/system-update-dsm-smoke.sh scripts/system-update-dsm-smoke.test.sh README.md deploy/README.md
git diff --cached --check
git commit -m "docs: 完善 Web 升级验收与运维说明"
```

## Final Review Gate

- [ ] Compare every section of `docs/superpowers/specs/2026-08-06-web-managed-upgrades-design.md` against Tasks 1-10 and confirm each requirement has an implementation/test owner.
- [ ] Run the repository unfinished-marker scan over all files changed by this plan and resolve every incomplete implementation marker.
- [ ] Run `git status --short` and verify only intended files remain.
- [ ] Inspect `docker compose config` and prove Docker socket appears exactly once under updater.
- [ ] Inspect updater API JSON and logs for Token、Cookie、password、DSN、digest、image ID、alias and stack leakage.
- [ ] Confirm release failure cannot move either `stable` tag before all immutable backend/web/updater images exist.
- [ ] Confirm updater has no host port, backend/web have no Docker socket, and postgres/updater are excluded from Web replacement and cleanup.
