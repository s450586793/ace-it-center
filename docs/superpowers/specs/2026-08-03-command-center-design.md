# Ace IT Center 命令中心设计

日期：2026-08-03

状态：已确认，待书面审阅

目标版本：Windows Agent `V0.4.0`

## 目标

在现有 Ace IT Center 中交付完整的远程命令闭环：Owner 在网页选择一台或多台 Windows 设备并下发 PowerShell 或 CMD 命令；服务端持久保存任务；在线 Agent 主动领取并以 LocalSystem 身份执行；执行状态、退出码、耗时和输出回传服务端并可在网页查看。

命令任务必须支持离线设备。设备离线时任务保持排队，上线后继续领取，不能依赖网页保持打开或单次心跳恰好成功。

## 首版范围

- 支持 Windows 10/11 x64 Agent。
- 支持 `powershell` 和 `cmd` 两种 Shell。
- 支持单设备和多设备批量下发。
- 支持排队、领取、运行、成功、失败和超时状态。
- 支持任务历史、每台设备的执行结果和失败任务重试。
- Agent 每台设备同时最多执行一个远程命令。
- 默认执行超时为 5 分钟，Owner 可设置 10 秒至 30 分钟。
- 命令文本最大 32 KiB，单次执行合并输出最大 256 KiB。

首版不支持 Linux Shell/Python、交互式终端、实时逐行流式输出、计划任务、审批流、命令模板和任务取消。Linux Agent 保持现有心跳行为，不领取 Windows 命令。

## 架构选择

采用 PostgreSQL 持久任务队列和 Agent 主动 HTTPS 长轮询。

未采用心跳响应夹带任务，因为心跳的设备状态上报与任务租约具有不同的重试、超时和并发语义，耦合后难以可靠处理离线排队及重复执行。未采用 WebSocket，因为首版不需要交互式终端，长连接会增加 DSM Nginx、反向代理和断线恢复的复杂度。

Agent 只发起出站 HTTP/HTTPS 请求，不要求设备开放入站端口。服务端使用事务和行锁原子领取任务，并通过租约处理 Agent 在领取后异常退出的情况。

## 数据模型

### command_tasks

一条记录表示 Owner 的一次下发动作：

- `id`：UUID 文本主键；
- `shell`：`powershell` 或 `cmd`；
- `command`：原始命令文本；
- `timeout_seconds`：10 至 1800；
- `created_by`：Owner ID；
- `created_at`：创建时间；
- `retried_from_id`：重试来源任务，可为空。

### command_executions

每个目标设备对应一条执行记录：

- `id`：UUID 文本主键；
- `task_id`、`node_id`：关联任务和设备；
- `status`：`queued`、`leased`、`running`、`succeeded`、`failed`、`timed_out`；
- `attempt`：领取次数，从 0 开始；
- `lease_token_hash`：本次租约随机令牌的哈希；
- `lease_expires_at`：租约失效时间；
- `started_at`、`finished_at`：运行时间；
- `exit_code`：进程退出码，可为空；
- `output`：UTF-8 合并输出；
- `output_truncated`：是否因 256 KiB 限制截断；
- `error_message`：安全、限长的失败摘要；
- `duration_ms`：执行耗时。

同一任务内 `task_id + node_id` 唯一。任务整体状态由执行记录汇总，不额外保存易失真的派生状态。

## 服务端 API

Owner API 继续使用现有 Session Cookie：

- `POST /api/v1/commands`：校验 Shell、命令、超时及目标设备，创建任务和执行记录；
- `GET /api/v1/commands`：按创建时间倒序返回任务及状态汇总；
- `GET /api/v1/commands/:id`：返回任务和所有设备执行详情；
- `POST /api/v1/commands/:id/retry`：仅为 `failed` 或 `timed_out` 的设备创建新任务。

Agent API 继续使用设备 Bearer Credential：

- `POST /api/v1/agent/commands/claim`：验证设备凭据，长轮询最多 20 秒并原子领取该设备最早的可执行任务；无任务返回 HTTP 204；
- `POST /api/v1/agent/commands/:id/start`：使用明文租约令牌把 `leased` 转为 `running`；
- `POST /api/v1/agent/commands/:id/complete`：使用租约令牌提交终态、退出码、输出、截断标记和耗时。

领取时服务端生成随机租约令牌，只向 Agent 返回一次，数据库仅保存哈希。租约为 35 分钟，覆盖最大命令超时及回传余量。已过期的 `leased` 执行可重新排队领取；已经进入 `running` 的执行不自动重复执行，避免机器重启后产生破坏性双重命令，改为标记失败并允许 Owner 手动重试。

所有 API 请求和响应使用有界 JSON。服务端错误不返回数据库、凭据或内部堆栈信息。

## Agent 执行模型

命令循环独立于 30 秒心跳循环。临时网络错误只影响下一次领取或结果回传，不停止 Windows Service，也不改变心跳在线状态。

执行器固定映射：

- PowerShell：`powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command <command>`；
- CMD：`cmd.exe /D /S /C <command>`。

Agent 不接受服务端传入任意可执行文件路径或额外启动参数。子进程继承 Windows Service 的 LocalSystem 权限；网页明确提示该权限具有高风险。命令超时时终止整个进程树，回传 `timed_out`。非零退出码回传 `failed`；退出码为 0 回传 `succeeded`。

标准输出和标准错误按产生顺序写入同一个有界缓冲区，统一解码为 UTF-8；无法解码的字节替换为 Unicode replacement character。达到 256 KiB 后继续读取并丢弃剩余输出，避免子进程因管道阻塞，同时设置 `output_truncated=true`。

Agent 在领取后先调用 `start`，再启动进程。终态回传遇到网络错误时使用相同租约令牌重试，直至服务端确认或租约失效。服务停止时不领取新任务，并给当前命令最多 10 秒正常退出时间，之后终止进程树。

## 网页命令中心

侧边栏新增“命令中心”，使用终端图标。页面包含：

- 设备多选，只列出 Windows 设备并显示在线/离线状态；
- PowerShell/CMD 分段选择；
- 等宽字体命令编辑框；
- 超时输入；
- 明确的高权限风险确认；
- “下发命令”主操作；
- 任务历史表，显示创建时间、Shell、目标数及各状态数量；
- 任务详情抽屉，逐设备显示状态、退出码、耗时、开始/结束时间和输出；
- 对失败或超时任务提供重试操作。

提交成功后清空设备选择但保留 Shell 和超时。任务历史在命令中心可见时每 5 秒刷新，页面隐藏时停止轮询。长输出在固定高度的可滚动等宽区域显示，不执行 ANSI 控制序列，不把输出作为 HTML 渲染。

## 安全与审计

- 只有已登录 Owner 能创建、查看和重试任务。
- Agent 通过现有 Credential 只能领取属于自身 Node ID 的执行记录。
- 命令及输出保存在 PostgreSQL，属于运维审计数据；界面提示不要在命令中写明文密码或 Token。
- 服务端日志只记录任务 ID、Node ID、状态和耗时，不记录命令正文、输出或租约令牌。
- 命令、输出、错误和请求体均有大小限制。
- 租约令牌、设备凭据不得进入命令行、Agent 日志或服务端日志。
- Web 输出只按纯文本展示，防止 XSS 和 ANSI 终端注入。

## 错误处理

- 创建任务时任何一个 Node ID 不存在或不是 Windows 设备，整个请求失败且不产生部分任务。
- 离线设备保持 `queued`，不判为失败。
- Agent 重复调用 `start` 或 `complete` 时，具有同一租约令牌且内容一致的请求按幂等成功处理。
- 租约错误或过期返回 HTTP 409，Agent 放弃本地结果并记录不含凭据的诊断信息。
- Agent 执行器启动失败回传 `failed` 和安全错误摘要。
- 数据库或 API 短暂不可用时，前端保留表单内容并显示可重试错误；Agent 退避后继续轮询。

## 测试策略

### 服务端

- 创建单设备和批量任务；
- 拒绝空命令、非法 Shell、非法超时、重复/不存在/Linux 目标；
- Owner API 未认证时返回 401；
- Agent 只能领取自己的任务；
- 并发领取不会返回同一执行；
- 无任务长轮询返回 204；
- 租约哈希、状态转换、幂等完成和过期处理正确；
- 重试只复制失败/超时目标；
- PostgreSQL Schema 可重复迁移。

### Agent

- Client 正确处理领取、204、启动和完成；
- PowerShell/CMD 使用固定可执行文件与参数；
- 成功、非零退出码、启动失败和超时映射正确；
- 超时终止进程树；
- 输出 UTF-8 归一化及 256 KiB 截断；
- 命令循环的网络错误不停止 Worker；
- Credential 和租约令牌不出现在错误及日志中。

### 前端

- 只能选择 Windows 设备且离线设备仍可排队；
- 表单校验、风险确认和成功提交行为正确；
- 历史状态汇总和详情输出正确显示；
- 重试仅在存在失败/超时目标时可用；
- 轮询只在命令中心可见时运行；
- 移动端无控件或文本重叠。

## 发布与验收

发布 `V0.4.0` Windows 安装包并更新稳定版 `latest.json`、网页下载文件名及左下角最新版本。通过 DSM `/volume4/docker/docker/ace-it-center/compose.yaml` 更新 backend 和 web，不创建脱离 Compose 项目的常驻容器。

线上验收至少使用一台手动安装 `V0.4.0` 的 Windows 设备完成：

1. 下发 PowerShell 成功命令并看到退出码 0 与输出；
2. 下发 CMD 非零退出命令并看到失败状态和退出码；
3. 下发超时命令并看到进程树终止和 `timed_out`；
4. 设备离线时创建任务，上线后自动执行；
5. 网页刷新或重新登录后历史和输出仍存在；
6. Agent Service 重启后心跳、日志上传和命令领取均恢复；
7. 下载页文件名、安装包版本、Agent 上报版本和稳定清单全部为 `V0.4.0`。

自动更新可靠性不以 `V0.3.8` 或 `V0.3.9` 升级到 `V0.4.0` 作为通过依据。需要在后续 `V0.4.1` 发布时，以真实 Windows 设备验证 `V0.4.0 -> V0.4.1` 的完整静默升级和 Service 恢复。
