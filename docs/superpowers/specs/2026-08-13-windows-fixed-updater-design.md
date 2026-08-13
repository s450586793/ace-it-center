# Windows Agent 固定更新器降误报设计

日期：2026-08-13
目标首版：Windows Agent `V0.4.11` 迁移版、`V0.4.12` 验证版

## 背景与根因

现有自动更新链路在功能和安全完整性上已经成立：Agent 从固定源读取 Ed25519 签名清单，限制下载大小，验证发布签名、版本、URL、文件大小和 SHA-256；安装前保存 last-known-good，安装后恢复 Service 配置并通过 named pipe 做健康检查，失败时回滚。`412-itx` 和 `ace-pc` 已真实完成 `V0.4.9 -> V0.4.10` 自动更新。

火绒提示针对的是更新器行为形态，而不是已确认的更新失败或木马检出。当前 Service 会把完整 `AceAgent.exe` 复制成随机名称 `.AceAgent-update-helper-<random>.exe`，然后以隐藏、detached、breakaway 进程运行。该进程停止 Service、运行安装器、替换程序并重新启动 Service。这些行为与恶意程序常见的自复制、随机落地和持久化修改相似；同时当前 Windows 二进制没有 Authenticode 代码签名，因此缺少 Windows 和杀毒软件可识别的发布者信誉。

本次不使用加壳、压缩或代码混淆。它们会进一步增加静态熵和行为可疑度，不能解决发布者信誉问题。

## 目标与非目标

目标：

- 安装目录始终提供固定名称 `AceAgentUpdater.exe`，不再为每次更新复制随机名称可执行文件。
- `AceAgent.exe` 保留每 `1 小时 + 0～10 分钟随机抖动`的调度，但把检查、下载、验证和安装执行委托给固定 Updater。
- 保留现有 Ed25519 清单验证、同源下载限制、SHA-256、大小限制、Service 健康检查、LKG 回滚、日志上传和手动检查功能。
- Agent、Updater 和安装器包含稳定且完整的 Windows 版本资源。
- 从 `V0.4.10` 平滑迁移，不要求用户手工卸载或重新配对。
- 通过连续两个版本验证迁移和新链路：`V0.4.11` 安装固定 Updater，`V0.4.12` 验证固定 Updater 完成自动更新。

非目标：

- 不承诺在没有 Authenticode 证书时所有杀毒软件零提示。
- 不关闭火绒、不添加全盘白名单，也不把 Agent 加壳。
- 不改变 Backend、Web、DSM Compose、数据库或设备协议。
- 不删除既有更新签名机制，也不让安装器自行信任未经 Agent/Updater 验证的远程内容。

## 进程与文件布局

安装目录：

```text
C:\Program Files\Ace IT Center\
├ AceAgent.exe
├ AceAgentUpdater.exe
└ AceAgentUpdater.next.exe   # 仅在 Updater 需要替换时短暂存在
```

数据目录保持不变：

```text
C:\ProgramData\AceITCenter\
├ agent.json
├ logs\agent.log
├ logs\update.log
└ updates\
   ├ AceAgentSetup-windows-amd64-V<version>.exe
   └ AceAgent.lkg.exe
```

`AceAgentUpdater.exe` 是独立、最小化的 Windows GUI subsystem 程序，不包含托盘、配对、心跳、远程命令或设备凭据逻辑。它只接受本机绝对路径和公开更新配置，不接收 enrollment token 或 Agent credential。

## 更新数据流

### 启动与定时检查

1. Agent Service 启动时先检查安装目录是否有 `AceAgentUpdater.next.exe`。
2. 若 `.next` 存在，Agent 先校验它必须是安装器写入的普通 PE 文件、位于同一受保护安装目录且版本不低于当前固定 Updater，再以原子替换方式更新 `AceAgentUpdater.exe`。旧 Updater 尚未退出导致文件占用时，Agent 在后台按固定上限重试；最终失败只写日志，不影响心跳。
3. Controller 在 Service 启动后立即触发一次更新，之后保持现有 `1 小时 + 0～10 分钟`周期。
4. Agent 同步调用固定 `AceAgentUpdater.exe check`，只传服务器 origin、当前 Agent 版本、Windows 版本和更新缓存目录。Updater 从 origin 获取 `/downloads/windows/stable/latest.json`，完成签名、版本、OS 和同源 URL 校验。
5. `check` 只通过受大小限制的 stdout JSON 和明确退出码返回结果，不混用日志。没有新版本时 Agent 保持在线，不进入 updating 状态。
6. 有新版本时 Updater 下载到 `updates` 目录，用现有上限和 SHA-256 校验后在 JSON 中返回候选版本、公开下载 URL 及本地安装包路径。

### 应用更新

1. Agent 获得 Controller generation 授权，避免配置切换、重复手动检查和定时检查并发启动更新。
2. Agent 以 detached 方式调用同一个固定 `AceAgentUpdater.exe apply`，传入安装包、当前 `AceAgent.exe`、LKG 路径和候选版本；不传任何凭据。
3. Updater 获取全局互斥锁，校验自身必须位于受保护的安装目录且文件名为 `AceAgentUpdater.exe`。
4. Updater停止托盘和 Agent Service，备份当前 Agent，使用 `/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /FORCECLOSEAPPLICATIONS /UPDATEHELPER` 运行已校验安装包。
5. 安装器写入新的 `AceAgent.exe`。若新 Updater 与正在运行的固定 Updater 不同，安装器写入 `AceAgentUpdater.next.exe`，不覆盖当前进程。新 Agent 上线后在后台重试原子替换，直到旧 Updater 完成健康检查并退出，或达到明确的重试上限。
6. Updater恢复 Service 配置，启动 Service，并等待 named pipe 在最多 60 秒内报告 healthy。
7. 成功后删除安装包和 LKG；下一次新 Agent 启动时完成 `.next -> AceAgentUpdater.exe` 原子替换。
8. 安装、Service 启动或健康检查失败时，Updater继续使用现有 LKG 恢复状态机恢复 `AceAgent.exe`、Service 配置和运行状态。

## 迁移兼容性

`V0.4.10` 不知道固定 Updater 的存在，因此 `V0.4.10 -> V0.4.11` 必须最后一次沿用旧的随机 helper 链路。`V0.4.11` 安装器会同时安装：

- 新的 `AceAgent.exe`；
- 固定 `AceAgentUpdater.exe`；
- 完整版本资源。

从 `V0.4.11` 开始，所有自动和手动更新都调用固定 Updater。随后发布 `V0.4.12`，以真实 `V0.4.11 -> V0.4.12` 更新验证随机 helper 已不再创建。旧 `.AceAgent-update-helper-*.exe` 仅做有界清理：只删除 `C:\ProgramData\AceITCenter\updates` 下满足精确前后缀、不是当前进程且超过安全时间窗口的遗留文件。

## Windows 版本资源

构建为 Agent 和 Updater 分别生成 `.syso` 版本资源，至少包含：

- `CompanyName`: `Ace IT Center`
- `ProductName`: `Ace IT Center Agent`
- `FileDescription`: `Ace IT Center Agent` / `Ace IT Center Agent Updater`
- `FileVersion`: 与 release semantic version 对应的四段 Windows 版本
- `ProductVersion`: release semantic version
- `OriginalFilename`: `AceAgent.exe` / `AceAgentUpdater.exe`
- `InternalName`: `AceAgent` / `AceAgentUpdater`
- `LegalCopyright`: `Copyright (C) 2026 Ace IT Center`

Inno Setup 继续设置 `AppPublisher`、`AppVersion` 和版本化安装包文件名，并补齐安装器 `VersionInfo*` 字段。资源不是代码签名，不能替代 Authenticode，但能避免匿名、无描述二进制。

## 安全边界

- Updater 的网络目标仍由配置中的 HTTPS/HTTP origin 派生，拒绝跨 origin 重定向、userinfo、异常端口和不匹配 URL。
- Updater 内嵌与 Agent 相同的 Ed25519 public key；只应用签名正确且版本递增的 manifest。
- 固定 Updater 和 `.next` 必须位于受保护的 Program Files 安装目录，更新缓存保持 SYSTEM/Administrators ACL。
- Agent 启动 Updater 时使用明确参数数组，不使用 shell，不传 credential。
- 全局 mutex 防止多个 Updater 同时修改 Service。
- 日志只记录公开版本、阶段和安全错误分类，不写 Token、credential、完整敏感 URL 或机器隐私数据。
- 未来获得 Authenticode 证书后，构建链路必须在发布前对 `AceAgent.exe`、`AceAgentUpdater.exe` 和最终安装器签名并使用可信时间戳；Ed25519 更新签名仍保留，二者职责不同。

## 错误处理

- Updater 不存在或无法启动：Agent 记录 `updater_unavailable`，继续心跳，并允许安装器修复；不得退回随机自复制逻辑。
- 更新检查失败：记录安全化阶段，按下个调度周期重试，不弹 Windows 通知。
- 候选下载或签名失败：删除 partial 文件，不进入 apply。
- `.next` 替换失败：保留旧 Updater，后台有限重试并记录错误；达到上限后留待下次 Service 启动重试，不阻止 Agent 上线。
- apply 启动后：沿用 Controller pending TTL，避免重复更新。
- 安装或健康检查失败：恢复 LKG；恢复也失败时上传主阶段和恢复阶段，但不上传底层敏感错误正文。

## 测试与发布验收

单元测试：

- Agent 启动固定路径 Updater，不调用随机副本 API。
- `check` 无更新、有更新、签名失败、跨源 URL、大小和 SHA-256 错误。
- `apply` 参数不包含 credential，固定身份和 mutex 在 Service 变更前校验。
- `.next` 原子替换成功、文件占用失败重试、无 `.next`、非法路径。
- 既有停止、安装、健康检查、LKG 回滚和日志安全测试全部保留。
- Windows 版本资源生成器验证 semantic version、四段版本转换和每个资源字段。

构建契约：

- 安装包 inventory 同时包含 `AceAgent.exe` 和 `AceAgentUpdater.exe`。
- 两个 PE 都是 amd64 Windows GUI binary，文件名固定且版本资源正确。
- 安装器补齐版本资源，升级不修改 `agent.json`。
- 源码和安装包不得包含 `WinDivert` 或任何内核驱动。

真实 Windows 验收：

1. 发布 `V0.4.11`，确认 `412-itx` 和 `ace-pc` 自动迁移、固定 Updater 安装成功并正常心跳。
2. 发布 `V0.4.12`，确认两台设备由固定 Updater 自动升级，`update.log` 记录成功。
3. 确认 `updates` 目录不再产生新的 `.AceAgent-update-helper-*.exe`。
4. 确认火绒不再出现随机 helper 的“系统加固”记录；若仍提示固定 Updater，则保留详情作为 Authenticode 证书和厂商申诉的输入，不能通过加壳规避。
5. DSM Compose 的 PostgreSQL、Backend、Web、Updater 保持 healthy；Agent 发布不重建 Web 或 Backend。

## 对现有更新方式的结论

现有方式不是更新完整性设计错误，也不是自动更新不可用。它的签名、哈希、回滚和健康检查方向正确，并已通过真实自动更新证明。需要修正的是进程职责和落地形态：主 Agent 不应每次复制成随机可执行文件充当安装协调器。固定、独立、职责最小的 Updater 更容易审计，也更符合 Windows 安全软件对合法自更新程序的预期。真正建立跨杀毒软件信誉仍需要 Authenticode 证书与持续稳定的签名发布历史。
