# 网络累计流量 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将网络监控表改为实时下载/上传加北京时间今日、本月累计流量，并由 Agent 在本地可靠持久化累计状态。

**Architecture:** Agent 从操作系统累计网络计数器计算增量，在本地原子保存日/月累计并随心跳上报。服务端向后兼容旧心跳，将最新累计状态保存到 `nodes`；前端直接使用节点字段渲染 8 列表格，现有速度历史曲线与接口保持不变。

**Tech Stack:** Go 1.26、gopsutil、Gin、PostgreSQL 16、Vue 3、TypeScript、Vitest、Docker Compose、Inno Setup。

## Global Constraints

- 日/月边界固定使用北京时间 UTC+8。
- 当前速度使用十进制 `MB/s`；累计量使用十进制 `B / KB / MB / GB / TB`。
- 离线设备当前速度显示 `-`，本周期累计值保留。
- 跨日或跨月后仍离线的设备，对应新周期显示 `0 B`。
- 旧 Agent 的累计字段显示 `-`，不得把默认零误报成真实 `0 B`。
- 现有速度历史抽屉及 `/api/v1/network/summary`、网络历史 API 保持兼容。
- Windows Agent 发布版本为 `0.3.8`，现有设备必须通过自动更新获得该版本。
- DSM Compose 项目路径固定为 `/volume4/docker/docker/ace-it-center`。
- 不覆盖或回退工作区中已有的未提交改动；仅暂存当前任务明确新增且无冲突的文件。

---

## File Map

- `agent/internal/agent/network.go`：读取操作系统计数器并计算当前速率。
- `agent/internal/agent/network_usage.go`：新增，负责北京时间周期、累计状态、加载和原子保存。
- `agent/internal/agent/network_usage_test.go`：新增，覆盖累计、重启、跨周期、回退和损坏状态。
- `agent/internal/agent/collector.go`：把实时速度和累计状态映射到心跳。
- `agent/cmd/ace-agent/main.go`：将配置目录中的状态路径与日志回调注入采集器。
- `internal/core/types.go`：定义 Agent、服务端、前端共享的累计字段契约。
- `internal/postgres/schema.sql`：为 `nodes` 添加最新累计状态列。
- `internal/postgres/store.go`：扫描和原子更新累计字段。
- `internal/api/router.go`：验证累计可用性、日期和旧 Agent 兼容规则。
- `frontend/src/lib/traffic.ts`：新增，北京时间周期判断和字节格式化。
- `frontend/src/components/NetworkMonitoring.vue`：渲染目标 8 列并移除 24 小时汇总轮询。
- `frontend/src/components/OperationsWorkspace.vue`：移除已不需要的网络汇总刷新键，更新稳定版回退版本。
- `frontend/src/types.ts`：声明节点累计字段。
- `frontend/src/style.css`：为 8 列表格设置稳定最小宽度。
- `frontend/src/App.test.ts`、`frontend/src/components/NodeTable.test.ts`、`frontend/src/components/PairingRequests.test.ts`、`frontend/src/components/NetworkHistoryDrawer.test.ts`、`frontend/src/components/NetworkMonitoring.test.ts`、`frontend/src/components/OperationsWorkspace.test.ts`：更新节点夹具、表头和离线/跨周期断言。

---

### Task 1: Agent 网络累计状态引擎

**Files:**
- Create: `agent/internal/agent/network_usage.go`
- Create: `agent/internal/agent/network_usage_test.go`
- Modify: `agent/internal/agent/network.go`
- Modify: `agent/internal/agent/network_test.go`

**Interfaces:**
- Consumes: `networkCounters{sent, received uint64}` 和注入的 `func() time.Time`。
- Produces: `NetworkSnapshot`、`NewNetworkUsageSampler(path string, report func(error))`、`Sample() NetworkSnapshot`。

- [ ] **Step 1: 写入首次采样与连续增量的失败测试**

```go
func TestNetworkUsageSamplerEstablishesBaselineThenAccumulates(t *testing.T) {
    beijing := time.FixedZone("Asia/Shanghai", 8*60*60)
    start := time.Date(2026, 8, 3, 9, 0, 0, 0, beijing)
    var saved networkUsageState
    sampler := newNetworkUsageSampler(
        sequenceReader([]networkReadResult{
            {counters: networkCounters{sent: 10_000_000, received: 20_000_000}},
            {counters: networkCounters{sent: 13_000_000, received: 28_000_000}},
        }),
        sequenceClock([]time.Time{start, start.Add(2 * time.Second)}),
        func() (networkUsageState, error) { return networkUsageState{}, os.ErrNotExist },
        func(state networkUsageState) error { saved = state; return nil },
        nil,
    )

    first := sampler.Sample()
    second := sampler.Sample()

    if !first.UsageAvailable || first.TodayUploadBytes != 0 || first.TodayDownloadBytes != 0 {
        t.Fatalf("first snapshot = %#v", first)
    }
    if second.UploadMBPerSecond != 1.5 || second.DownloadMBPerSecond != 4 ||
        second.TodayUploadBytes != 3_000_000 || second.TodayDownloadBytes != 8_000_000 ||
        second.MonthUploadBytes != 3_000_000 || second.MonthDownloadBytes != 8_000_000 {
        t.Fatalf("second snapshot = %#v", second)
    }
}
```

- [ ] **Step 2: 运行测试并确认 RED**

Run: `go test ./agent/internal/agent -run 'TestNetworkUsageSamplerEstablishesBaselineThenAccumulates' -count=1`

Expected: FAIL，`newNetworkUsageSampler` 和 `NetworkSnapshot` 尚未定义。

- [ ] **Step 3: 实现最小状态模型与采样接口**

```go
type NetworkSnapshot struct {
    UploadMBPerSecond   float64
    DownloadMBPerSecond float64
    UsageAvailable      bool
    UsageDay            string
    TodayUploadBytes    uint64
    TodayDownloadBytes  uint64
    MonthUploadBytes    uint64
    MonthDownloadBytes  uint64
}

type networkUsageState struct {
    Schema                int    `json:"schema"`
    Day                   string `json:"day"`
    Month                 string `json:"month"`
    TodayUploadBytes      uint64 `json:"today_upload_bytes"`
    TodayDownloadBytes    uint64 `json:"today_download_bytes"`
    MonthUploadBytes      uint64 `json:"month_upload_bytes"`
    MonthDownloadBytes    uint64 `json:"month_download_bytes"`
    PreviousSentBytes     uint64 `json:"previous_sent_bytes"`
    PreviousReceivedBytes uint64 `json:"previous_received_bytes"`
    Initialized           bool   `json:"initialized"`
}
```

`Sample` 必须先按当前 UTC+8 时间更新 `Day`/`Month`，只对不回退的计数器计算正增量；实时速率使用进程内基线，首次采样为零。

- [ ] **Step 4: 增加重启、跨日、跨月和计数器回退失败测试**

测试必须明确断言：加载已有状态后继续累计；日期变化只清零日累计；月份变化同时清零日/月累计；任一计数器变小则本轮两个方向均重新建立基线且不增加累计。

- [ ] **Step 5: 运行新增测试并确认 RED**

Run: `go test ./agent/internal/agent -run 'TestNetworkUsageSampler(Resumes|RollsDay|RollsMonth|Resets)' -count=1`

Expected: FAIL，周期和恢复行为尚未实现。

- [ ] **Step 6: 实现周期切换和状态恢复**

使用固定时区：

```go
var beijingLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

func usagePeriod(now time.Time) (day, month string) {
    local := now.In(beijingLocation)
    return local.Format("2006-01-02"), local.Format("2006-01")
}
```

加载状态的 `Schema` 非当前版本或 JSON 解码失败时，通过 `report(error)` 记录并从空状态恢复；`os.ErrNotExist` 是正常首次运行，不记录错误。

- [ ] **Step 7: 写入真实文件原子保存失败测试**

使用 `t.TempDir()` 断言保存后可重新加载；注入失败的 `rename`/保存函数，断言 `Sample` 仍返回内存中的累计值、回调收到不含敏感信息的错误、下一次采样再次尝试保存。

- [ ] **Step 8: 实现 `LoadNetworkUsageState` 和 `SaveNetworkUsageState`**

状态路径为空时使用内存模式；非空时创建 `0700` 目录、创建临时文件、写 JSON、关闭文件、应用现有 `secureConfigFile` 权限策略并原子替换目标。Windows 替换前沿用配置文件的“打开并关闭旧文件”兼容流程。

- [ ] **Step 9: 运行 Agent 包测试并确认 GREEN**

Run: `go test ./agent/internal/agent -count=1`

Expected: PASS。

---

### Task 2: 心跳契约与 Agent 运行时接线

**Files:**
- Modify: `internal/core/types.go`
- Modify: `agent/internal/agent/collector.go`
- Modify: `agent/internal/agent/collector_test.go`
- Modify: `agent/cmd/ace-agent/main.go`
- Modify: `agent/cmd/ace-agent/main_test.go`
- Modify: `agent/internal/app/worker_test.go`

**Interfaces:**
- Consumes: Task 1 的 `NetworkSnapshot`。
- Produces: 心跳 JSON 字段 `network_usage_available`、`network_usage_day`、`network_today_*_bytes`、`network_month_*_bytes`。

- [ ] **Step 1: 扩展采集器测试并确认 RED**

```go
if !second.NetworkUsageAvailable || second.NetworkUsageDay != "2026-08-03" ||
    second.NetworkTodayUploadBytes != 3_000_000 || second.NetworkTodayDownloadBytes != 8_000_000 ||
    second.NetworkMonthUploadBytes != 3_000_000 || second.NetworkMonthDownloadBytes != 8_000_000 {
    t.Fatalf("network usage heartbeat = %#v", second)
}
```

Run: `go test ./agent/internal/agent -run TestHostCollectorSetsNetworkCapabilityAndSamplesConsecutively -count=1`

Expected: FAIL，`core.Heartbeat` 尚无累计字段。

- [ ] **Step 2: 扩展共享类型和采集器映射**

在 `core.Heartbeat` 与 `core.Node` 中添加：

```go
NetworkUsageAvailable      bool   `json:"network_usage_available"`
NetworkUsageDay            string `json:"network_usage_day"`
NetworkTodayUploadBytes    uint64 `json:"network_today_upload_bytes"`
NetworkTodayDownloadBytes  uint64 `json:"network_today_download_bytes"`
NetworkMonthUploadBytes    uint64 `json:"network_month_upload_bytes"`
NetworkMonthDownloadBytes  uint64 `json:"network_month_download_bytes"`
```

- [ ] **Step 3: 写入状态路径和日志注入失败测试**

断言 `networkUsagePath("C:\\ProgramData\\AceITCenter\\agent.json")` 与配置文件同目录并以 `network-usage.json` 结尾；构造 `serviceWorker` 时必须传入配置路径，采集状态错误通过现有 `slog.Logger.Warn` 输出。

- [ ] **Step 4: 实现服务和前台模式接线**

```go
func networkUsagePath(configPath string) string {
    return filepath.Join(filepath.Dir(configPath), "network-usage.json")
}
```

`serviceWorker` 增加 `configPath string`，服务模式和前台模式都用各自实际 `configPath` 创建持久化采集器；`--once` 也使用相同路径，不建立第二份状态。

- [ ] **Step 5: 运行 Agent 相关测试并确认 GREEN**

Run: `go test ./agent/cmd/ace-agent ./agent/internal/agent ./agent/internal/app -count=1`

Expected: PASS。

---

### Task 3: 服务端数据库迁移与心跳存储

**Files:**
- Modify: `internal/postgres/schema.sql`
- Modify: `internal/postgres/store.go`
- Modify: `internal/postgres/store_test.go`
- Modify: `internal/postgres/migration_integration_test.go`

**Interfaces:**
- Consumes: Task 2 的 `core.Heartbeat` 累计字段。
- Produces: `ListNodes` 和 `RecordHeartbeat` 返回完整累计字段。

- [ ] **Step 1: 写入真实 PostgreSQL 迁移/心跳失败测试**

扩展 `TestRecordHeartbeatWithCredentialJoinUpdatesNodeAndNetworkSample`，心跳携带日期 `2026-08-03` 和四个字节值；断言返回节点与数据库 `nodes` 行完全一致，同时 `network_samples` 的速度样本仍存在。

- [ ] **Step 2: 运行集成测试并确认 RED**

Run: `docker run --name ace-it-network-usage-test-db -e POSTGRES_USER=ace -e POSTGRES_PASSWORD=ace-test -e POSTGRES_DB=ace_it_center -p 127.0.0.1:55432:5432 -d postgres:16-alpine`

等待 `pg_isready` 成功后设置：`ACE_TEST_POSTGRES_DSN=postgres://ace:ace-test@127.0.0.1:55432/ace_it_center?sslmode=disable`。

Run: `go test ./internal/postgres -run TestRecordHeartbeatWithCredentialJoinUpdatesNodeAndNetworkSample -count=1 -v`

Expected: FAIL，数据库列不存在或返回节点缺少累计字段。

- [ ] **Step 3: 添加幂等迁移列**

```sql
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS network_usage_available BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS network_usage_day TEXT NOT NULL DEFAULT '';
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS network_today_upload_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS network_today_download_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS network_month_upload_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS network_month_download_bytes BIGINT NOT NULL DEFAULT 0;
```

- [ ] **Step 4: 扩展统一节点列和扫描顺序**

`nodeColumns` 与 `scanNode` 按相同顺序加入 6 个字段；所有引用继续使用 `nodes.` 限定，避免 `UPDATE ... FROM node_credentials` 再次发生字段歧义。

- [ ] **Step 5: 扩展心跳事务更新**

`RecordHeartbeat` 在同一事务内更新累计可用性、日期、四个计数器和 `last_seen_at`，并继续写入实时速度样本。不得拆成两个提交。

- [ ] **Step 6: 运行单元和真实数据库测试并确认 GREEN**

Run: `go test ./internal/postgres -count=1`

Run: `go test ./internal/postgres -run 'TestMigrate|TestRecordHeartbeatWithCredentialJoinUpdatesNodeAndNetworkSample' -count=1 -v`，环境中保留上一步的 `ACE_TEST_POSTGRES_DSN`。

Expected: 两条命令均 PASS。

---

### Task 4: 心跳校验与旧 Agent 兼容

**Files:**
- Modify: `internal/api/router.go`
- Modify: `internal/api/router_test.go`

**Interfaces:**
- Consumes: Task 2 的心跳字段和 Task 3 的原子存储。
- Produces: 旧心跳成功、有效新心跳成功、矛盾累计负载返回 HTTP 400。

- [ ] **Step 1: 写入兼容和校验失败测试**

新增表驱动测试：

```go
tests := []struct {
    name string
    body string
    want int
}{
    {"legacy", `{"hostname":"pc"}`, http.StatusOK},
    {"valid", `{"hostname":"pc","network_usage_available":true,"network_usage_day":"2026-08-03","network_today_download_bytes":10}`, http.StatusOK},
    {"missing day", `{"network_usage_available":true}`, http.StatusBadRequest},
    {"invalid day", `{"network_usage_available":true,"network_usage_day":"2026-02-30"}`, http.StatusBadRequest},
    {"disabled with counters", `{"network_usage_available":false,"network_today_download_bytes":10}`, http.StatusBadRequest},
    {"negative counter", `{"network_usage_available":true,"network_usage_day":"2026-08-03","network_today_download_bytes":-1}`, http.StatusBadRequest},
}
```

- [ ] **Step 2: 运行 API 测试并确认 RED**

Run: `go test ./internal/api -run 'TestAgentHeartbeat(NetworkUsage|AcceptsLegacy)' -count=1 -v`

Expected: FAIL，无效日期或矛盾字段仍被接受。

- [ ] **Step 3: 实现严格校验**

`network_usage_available=false` 时要求日期为空且四个累计值全为零；为 `true` 时用 `time.Parse("2006-01-02", value)` 验证真实日期。负数在 JSON 绑定到 `uint64` 时直接触发 HTTP 400。校验必须在调用 repository 前完成。

- [ ] **Step 4: 运行 API 与 PostgreSQL 测试并确认 GREEN**

Run: `go test ./internal/api ./internal/postgres -count=1`

Expected: PASS。

---

### Task 5: 前端 8 列累计流量表

**Files:**
- Create: `frontend/src/lib/traffic.ts`
- Create: `frontend/src/lib/traffic.test.ts`
- Modify: `frontend/src/types.ts`
- Modify: `frontend/src/components/NetworkMonitoring.vue`
- Modify: `frontend/src/components/NetworkMonitoring.test.ts`
- Modify: `frontend/src/components/OperationsWorkspace.vue`
- Modify: `frontend/src/components/OperationsWorkspace.test.ts`
- Modify: `frontend/src/components/AgentDownloads.vue`
- Modify: `frontend/src/components/AgentDownloads.test.ts`
- Modify: `frontend/src/style.css`
- Modify: `frontend/src/App.test.ts`
- Modify: `frontend/src/components/NodeTable.test.ts`
- Modify: `frontend/src/components/PairingRequests.test.ts`
- Modify: `frontend/src/components/NetworkHistoryDrawer.test.ts`

**Interfaces:**
- Consumes: Task 3 节点 JSON 字段。
- Produces: `formatTrafficBytes(value: number): string`、`beijingUsageDay(now: Date): string`、目标 8 列表格。

- [ ] **Step 1: 写入字节格式与北京时间边界失败测试**

```ts
expect(formatTrafficBytes(999)).toBe('999 B')
expect(formatTrafficBytes(1_000)).toBe('1.00 KB')
expect(formatTrafficBytes(1_500_000_000)).toBe('1.50 GB')
expect(beijingUsageDay(new Date('2026-08-02T15:59:59Z'))).toBe('2026-08-02')
expect(beijingUsageDay(new Date('2026-08-02T16:00:00Z'))).toBe('2026-08-03')
```

Run: `npm test -- --run src/lib/traffic.test.ts`

Expected: FAIL，模块尚不存在。

- [ ] **Step 2: 实现格式器和周期辅助函数**

`formatTrafficBytes` 对非有限值和负数返回 `-`，使用 1000 进位且最多到 TB；`beijingUsageDay` 通过时间戳加 8 小时后截取 ISO 日期，不能依赖浏览器本地时区。

- [ ] **Step 3: 写入目标表格失败测试**

测试必须逐项断言 8 个表头的文本和顺序，并覆盖：在线当前速度、离线当前速度 `-`、离线同周期累计保留、跨日今日 `0 B`、同月月累计保留、跨月月累计 `0 B`、旧 Agent 四个累计单元格为 `-`。

Run: `npm test -- --run src/components/NetworkMonitoring.test.ts`

Expected: FAIL，仍渲染 24 小时平均/峰值列并请求汇总 API。

- [ ] **Step 4: 修改节点类型和网络表格**

从 `NetworkMonitoring.vue` 删除 `NetworkSummary*` 状态、`loadSummary`、30 秒汇总轮询和错误提示。保留上方实时合计、行点击历史抽屉和 `isNodeOnline`。表头必须为：

```ts
['设备', '状态', '当前下载', '当前上传', '今日总下载', '今日总上传', '本月总下载', '本月总上传']
```

累计显示函数先判断 `network_usage_available`，再比较 `network_usage_day` 与当前北京时间日/月，最后格式化对应字节值。

- [ ] **Step 5: 清理工作台汇总刷新和更新版本回退值**

移除 `networkRefreshKey` 及传给网络组件的 `refreshKey`；手动刷新仍通过既有 `emit('refresh')` 获取节点。将 `OperationsWorkspace.vue` 和 `AgentDownloads.vue` 的稳定版回退值及测试期望更新为 `0.3.8`，真实页面仍优先使用签名发布清单。

- [ ] **Step 6: 调整稳定布局并运行前端测试**

将 `.network-table` 的最小宽度提高到能容纳 8 个长中文表头的固定值，保留 `.table-wrap` 横向滚动；不缩放字体、不让单元格文字覆盖。

Run: `npm test -- --run`

Run: `npm run build`

Expected: 全部 PASS，构建退出码 0。

---

### Task 6: 全量验证、Windows Agent 0.3.8 发布与 DSM 部署

**Files:**
- Verify: `deploy/windows-builder.compose.yaml`
- Verify: `scripts/build-windows-agent.sh`
- Verify: `scripts/publish-windows-release.sh`
- Deploy: `/volume4/docker/docker/ace-it-center`

**Interfaces:**
- Consumes: Tasks 1-5 的完整实现。
- Produces: 已签名的 `AceAgentSetup-windows-amd64-V0.3.8.exe`、稳定发布清单和线上 Compose 服务。

- [ ] **Step 1: 运行格式检查和全量测试**

Run: `gofmt -w agent/internal/agent/network.go agent/internal/agent/network_usage.go agent/internal/agent/network_test.go agent/internal/agent/network_usage_test.go agent/internal/agent/collector.go agent/internal/agent/collector_test.go agent/cmd/ace-agent/main.go agent/cmd/ace-agent/main_test.go internal/core/types.go internal/postgres/store.go internal/postgres/store_test.go internal/postgres/migration_integration_test.go internal/api/router.go internal/api/router_test.go`

Run: `go test ./... -count=1`

Run with workdir `frontend`: `npm test -- --run`

Run with workdir `frontend`: `npm run build`

Run: `git diff --check`

Expected: 所有命令退出码 0；不得重置或清理无关工作区改动。

- [ ] **Step 2: 在 DSM 构建并发布签名安装包**

在 `/volume4/docker/docker/ace-it-center` 同步当前源文件后执行 Windows builder Compose，设置：

```text
RELEASE_VERSION=0.3.8
ACE_RELEASES_DIR=/volume4/docker/docker/ace-it-center/releases
```

先单独运行 `git rev-parse --short HEAD`，再将其原样设置为 `RELEASE_COMMIT`，不得使用空值或手写版本标识。

验证 `latest.json` 的版本、URL、SHA-256、签名和安装包文件名全部一致；不得覆盖已有不可变版本目录。

- [ ] **Step 3: 使用正式 Compose 更新 backend 和 web**

Run: `sudo docker compose build backend web`

Run: `sudo docker compose up -d --no-deps backend web`

Expected: `ace-it-center-backend-1`、`ace-it-center-web-1`、`ace-it-center-postgres-1` 均为 `healthy`。

- [ ] **Step 4: 验证公开接口和数据库迁移**

验证 `/api/v1/health` 返回 HTTP 200；查询目标节点确认累计可用性、日期和四个计数器已入库；Nginx 日志中的心跳持续返回 HTTP 200，无 SQL 错误。

- [ ] **Step 5: 验证现有 Agent 自动更新**

不手动安装，观察至少一台在线的 `0.3.7` 设备在自动检查周期内升级到 `0.3.8`。核对服务持续运行、心跳恢复、Agent 日志上传，并确认首次累计为 `0 B`，随后网络活动使日/月累计单调增加。

- [ ] **Step 6: 浏览器视觉验收**

用 Playwright 在桌面和移动视口截取网络监控页面，确认 8 个表头顺序正确、横向滚动可用、无重叠；在线当前速度、离线 `-`、今日/月累计显示符合设计。

- [ ] **Step 7: 清理仅用于测试/构建的临时容器**

先按明确名称列出本次创建的测试数据库、Go 测试和 Windows builder 容器，再删除这些临时容器及专用缓存卷。不得删除 Ace IT Center 正式 Compose 容器、PostgreSQL 数据卷、发布目录或签名密钥。
