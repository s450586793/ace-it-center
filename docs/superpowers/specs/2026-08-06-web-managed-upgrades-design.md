# Ace IT Center Web 手动升级设计

日期：2026-08-06

状态：已确认，待书面审阅

## 目标

为 Ace IT Center 增加仅 Owner 可用的“系统升级”页面。页面自动检查公开 GHCR 中的最新稳定版，Owner 确认后由独立 updater 服务升级 backend 和 web。升级成功并通过健康检查后，updater 精准删除本次被替换的本地旧镜像；升级失败时保留旧镜像并自动回滚。

源码仓库继续保持 GitHub Private。backend、web 和 updater 容器镜像发布到 Public GHCR，使 DSM 不需要保存 GitHub 或 GHCR Token。

## 首版范围

- 显示当前 backend/web 版本和 GHCR 最新稳定版本。
- 仅在 backend 与 web 的稳定版本一致时允许升级。
- 由已登录 Owner 手动确认并启动升级。
- 显示检查、备份、拉取、切换、健康检查、回滚和清理进度。
- 同一时间最多执行一个升级任务。
- backend/web 重启或浏览器刷新后继续显示真实任务状态。
- 升级成功后只删除本次被替换的 backend/web 本地旧镜像。
- 升级失败时恢复原 backend/web 镜像，并保留新旧镜像供人工排查。

首版不支持自动定时升级、无人值守升级、降级选择、任意版本选择、updater 自我升级、PostgreSQL 镜像升级、跨 Compose 项目管理或全局 Docker 清理。

## 发布模型

### GitHub 与 GHCR

- 源码仓库为 GitHub Private。
- GitHub Actions 只在已确认的 SemVer 版本标签上运行正式发布。
- 发布流水线先完成测试和构建，再推送 backend、web 和 updater 镜像。
- backend 与 web 同时发布不可变版本标签，例如 `v0.4.1`。
- 正式发布成功后，将 backend 与 web 的 `stable` 标签同时移动到该版本。
- updater 只发布不可变版本标签，由 DSM Compose 明确指定版本，不发布供 Web 升级使用的 `stable` 标签。
- Public GHCR 镜像不得包含 `.env`、数据库、DSM 凭据、签名私钥、GitHub Token 或业务数据。
- 镜像写入标准 OCI 标签，包括版本、Git commit 和构建时间。

updater 只读取公开 GHCR，不持有 Registry 写权限。DSM 首次部署和后续拉取公开镜像均不需要 Registry 登录凭据。

### Compose 服务

Compose 项目包含：

- `postgres`：现有持久数据库，不参与 Web 升级；
- `backend`：使用 `ghcr.io/s450586793/ace-it-center-backend:stable`；
- `web`：使用 `ghcr.io/s450586793/ace-it-center-web:stable`；
- `updater`：使用显式版本标签，不通过 Web 自我升级。

backend 和 web 保留 `pull_policy: always`。updater 不发布 DSM 端口，只加入 Compose 内部网络。updater 挂载：

- Docker socket，用于固定范围内的镜像与容器操作；
- Compose 配置的只读副本；
- 独立状态卷，用于原子保存升级任务状态；
- 升级备份目录，用于保存升级前 PostgreSQL 备份。

普通 DSM 启动仍使用基础 Compose 中的 `stable` 标签。执行升级时，updater 在状态卷内生成只含固定服务名和固定镜像引用的任务级 Compose override：目标 override 使用检查阶段记录的 `repo@sha256:digest`，回滚 override 使用任务开始时创建的本地回滚别名。两类 override 均将 backend/web 的 `pull_policy` 覆盖为 `never`；目标镜像必须先由 updater 显式拉取并校验，回滚只使用已记录的本地旧镜像。这样容器切换不再受执行期间 `stable` 标签再次移动或 Registry 临时不可用影响。

updater 自身需要升级时，由 Owner 在 DSM Container Manager 中手动更新 Compose 项目。Web 升级流程只替换 backend 和 web，避免执行中的 updater 自我重启。

## 架构与权限边界

调用链为：

```text
Owner 浏览器 -> backend Owner API -> updater 内部 API -> Docker Engine
```

web 是静态 Nginx 容器，不接触 Docker。backend 继续使用现有 Session Cookie 完成 Owner 鉴权，但不挂载 Docker socket。只有 updater 挂载 Docker socket。

backend 与 updater 使用独立的随机内部凭据。该凭据只存在于 DSM 环境配置中，不返回浏览器，不写入普通日志。updater 只暴露固定操作：

- 查询当前任务状态；
- 检查最新稳定版本；
- 启动升级到当前 GHCR `stable` 版本。

updater 不接受镜像名、容器名、Shell 命令、Compose 文件路径或 Docker API 路径等任意用户输入。它只能管理带有 `com.docker.compose.project=ace-it-center` 标签的 backend 和 web 服务，并拒绝操作 postgres、updater 及其他项目。

## 版本发现

updater 匿名读取 Public GHCR 中 backend/web 的 `stable` manifest 和 OCI 标签。只有满足以下条件才返回“可升级”：

- backend 与 web 都存在 `stable` manifest；
- 两个镜像的 OCI 版本完全一致；
- 版本是有效 SemVer；
- 目标版本高于当前运行版本；
- 当前没有正在执行的升级任务。

当前版本来自运行中 backend/web 镜像的 OCI 标签。两个运行版本不一致时，页面显示配置异常并禁止继续升级。检查结果采用短时缓存；Registry 超时或数据不完整时显示可重试错误，不把“检查失败”显示为“已是最新版”。

为避免检查和拉取之间 `stable` 标签变化，updater 在检查阶段保存目标 digest，拉取后重新校验版本和 digest。结果不一致时终止升级，不切换容器。

## Owner API 与页面

backend 增加 Owner API：

- `GET /api/v1/system/update`：返回当前版本、最新稳定版本和升级任务状态；
- `POST /api/v1/system/update/check`：请求 updater 重新检查稳定版本；
- `POST /api/v1/system/update`：确认并启动升级。

所有接口复用现有 Owner Session。未登录请求返回 HTTP 401。启动接口要求目标版本与最近一次检查结果一致，并拒绝重复提交。

前端侧边栏增加“系统升级”入口。页面显示：

- 当前版本；
- 最新稳定版本和发布时间；
- “检查更新”操作；
- 有新版本时可用的“立即升级”操作；
- 二次确认；
- 当前升级阶段、开始时间和安全错误摘要；
- 成功、回滚成功或需要人工处理的终态。

升级开始后页面轮询任务状态。backend/web 重启造成短暂断线时，页面保持当前阶段并自动重试。重新登录或刷新后，页面从 updater 恢复任务状态，不依赖浏览器内存推断结果。

## 升级任务状态

updater 使用持久状态卷原子保存一条当前或最近任务。任务至少包含：

- 任务 ID；
- 原版本与目标版本；
- 原 backend/web 镜像 ID；
- 原 backend/web 镜像的任务级本地回滚别名；
- 目标 backend/web digest；
- 当前阶段；
- 创建、开始和结束时间；
- 是否已回滚；
- 是否已清理旧镜像；
- 安全、限长的错误摘要。

阶段为：

```text
checking -> backing_up -> pulling -> switching_backend -> checking_backend
-> switching_web -> checking_web -> stabilizing -> cleaning -> succeeded
```

发生错误时进入：

```text
rolling_back -> failed
```

回滚也失败时进入 `manual_intervention`。终态写入状态卷后才向 backend 报告完成。

## 升级事务

1. 验证内部凭据、单任务锁和 Owner 已确认的目标版本。
2. 记录当前 backend/web 镜像 ID、OCI 版本和容器配置，并为两个旧镜像创建仅本任务使用的本地回滚别名。
3. 对当前 PostgreSQL 执行固定、无交互的升级前备份；备份失败则中止。
4. 按检查阶段保存的 digest 拉取 backend 与 web 镜像。
5. 再次验证两个镜像版本、digest 和允许的仓库名称。
6. 使用固定目标 digest 的任务级 Compose override 切换 backend，不重建 postgres 或 updater。
7. 等待 backend 容器 healthy，并验证 `/api/v1/health`。
8. 切换 web，等待 web 容器 healthy，并通过 web 反向代理验证公开健康接口。
9. 在稳定观察窗口内持续确认 backend、web 和数据库连接正常。
10. 将任务标记为可清理，然后删除本次被替换的两个本地旧镜像。
11. 记录新版本与清理结果，任务进入 `succeeded`。

数据库 Schema 由 backend 启动时的现有幂等迁移执行。所有允许通过 Web 发布的数据库变更必须向后兼容旧 backend。包含破坏性或不可回滚迁移的版本不得移动 `stable` 标签，也不得通过 Web 升级。

## 旧镜像清理

清理必须满足全部条件：

- backend 和 web 已切换到目标 digest；
- 两个服务均 healthy；
- 公开健康检查通过；
- 稳定观察窗口完成；
- 旧镜像 ID 未被任何容器引用；
- 任务尚未进入回滚或人工处理状态。

updater 只删除任务开始时记录的 backend/web 旧镜像 ID，以及指向这些 ID 的本项目旧别名和任务级回滚别名。清理顺序为先删除已记录别名，再在确认没有容器引用后删除对应镜像 ID。不得使用无范围的 `docker image prune`，不得根据名称模糊匹配，不得删除 BuildKit cache、PostgreSQL、updater、Windows builder 或其他 Compose 项目镜像。

若旧镜像仍被容器引用，升级本身仍可成功，但任务记录“旧镜像清理待处理”，页面明确显示未清理原因。updater 不使用强制删除。

## 失败与回滚

- 版本检查、备份或拉取失败：不修改当前容器，不删除任何镜像。
- backend 切换或健康检查失败：使用 `pull_policy: never` 的回滚 override 和任务级本地别名恢复两个服务。
- web 切换或健康检查失败：使用同一回滚机制恢复 backend 和 web，避免前后端版本不一致。
- 回滚期间保留新旧全部镜像，不进行清理。
- 回滚成功后任务进入 `failed`，页面显示安全摘要并允许重新检查更新。
- 回滚失败后任务进入 `manual_intervention`，禁止继续发起升级，直到 DSM 管理员处理并显式清除阻塞状态。
- updater 重启后读取持久状态；对非终态任务先检查实际容器和镜像状态，再继续、回滚或进入人工处理，不盲目重复切换。

浏览器只收到阶段、版本、时间和安全错误码。Docker socket 路径、Registry 内部响应、凭据、数据库 DSN、容器环境变量和堆栈信息不得返回前端。

## 安全要求

- 只有已登录 Owner 能查看版本或发起升级。
- backend 不挂载 Docker socket。
- updater 不开放宿主机端口。
- updater 内部 API 使用随机凭据和恒定时间比较。
- updater 镜像使用最小运行环境和非必要能力移除；Docker socket 权限属于已知的高权限边界。
- 所有可变输入采用长度限制和严格 SemVer/枚举校验。
- GHCR 镜像为 Public，但源码仓库保持 Private。
- Public 镜像视为可被任何人下载和检查，发布流水线必须验证其中不含秘密或业务数据。
- 日志记录任务 ID、阶段、版本和结果，不记录内部凭据、Cookie、环境变量或 Registry Token。

## 测试策略

### updater

- 匿名读取 Public GHCR `stable` manifest；
- 拒绝 backend/web 版本不一致、非法 SemVer、降级和 digest 变化；
- 单任务锁拒绝并发升级；
- 状态文件原子写入和重启恢复；
- 备份失败不切换容器；
- backend/web 成功切换和健康检查；
- 各阶段失败时恢复原镜像；
- 回滚失败进入 `manual_intervention`；
- 成功时只删除记录的两个旧镜像；
- 被引用的旧镜像不强制删除；
- postgres、updater 和其他项目镜像永不进入删除调用。

Docker 和 Registry 依赖使用受控 fake/mock，覆盖超时、网络失败和重复请求。另设 DSM 集成测试验证真实 Docker Compose 标签、镜像引用和容器重建行为。

### backend

- 未登录系统升级 API 返回 401；
- Owner 可检查版本、查看任务和启动升级；
- 目标版本与检查结果不一致时拒绝；
- updater 不可用时返回安全、可重试错误；
- backend 重启后仍能从 updater 恢复任务状态；
- 内部凭据和 updater 原始错误不进入 API 响应或日志。

### frontend

- 显示当前版本和最新稳定版本；
- 无新版本、检查失败或任务执行中时正确禁用升级按钮；
- 二次确认后只提交一次升级；
- 显示所有升级阶段和终态；
- backend/web 重启断线后自动恢复轮询；
- 非 Owner/未登录状态不显示可用升级入口；
- 桌面与移动端无控件、状态文字或版本号重叠。

## DSM 验收

1. Compose 项目包含 postgres、backend、web 和 updater，全部处于预期健康状态。
2. DSM 未配置 GHCR Token 仍能拉取 Public 镜像。
3. Owner 页面能识别当前版本和新的 `stable` 版本。
4. 启动升级后页面跨 backend/web 重启保持可恢复状态。
5. backend/web 使用同一目标版本并通过内部与公开健康检查。
6. PostgreSQL 容器 `StartedAt` 不变，数据卷未重建，升级前备份存在。
7. 成功后本地旧 backend/web 镜像被删除，当前镜像保留。
8. postgres、updater、Windows builder 和其他项目镜像的 ID 完全未变。
9. 注入 backend 或 web 健康失败时自动恢复旧版本，旧镜像不被删除。
10. 注入回滚失败时页面进入人工处理状态并阻止再次升级。

## 运维规则

- 正式升级只能使用 GitHub Actions 已发布并移动到 `stable` 的版本。
- updater 自身升级继续在 DSM Container Manager 中手动执行。
- Web 升级页不替代 DSM 数据备份、磁盘监控和灾难恢复流程。
- 首次上线该功能时清理现有 `pre-v*` 镜像前，必须确认没有容器引用，并仅处理 ace-it-center backend/web 仓库。
