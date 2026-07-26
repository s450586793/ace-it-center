# Ace Agent 客户端下载页设计

## 背景

Ace IT Center 已通过 `/downloads/` 提供 Windows 和 Linux Agent 二进制文件，但平台界面没有可发现的下载入口。管理员只能依赖外部说明获取链接，也无法在同一工作流中理解平台差异并继续生成设备接入令牌。

本次改动在登录后的工作台中增加独立“客户端下载”视图，让管理员完成“选择平台、下载 Agent、生成接入令牌”的连续操作。

## 目标

- 在登录后的侧边栏提供稳定、清晰的客户端下载入口。
- 展示 Windows x64 和 Linux x64 的平台、架构、文件名与下载操作。
- 说明 Ace Agent 当前负责设备注册、资产采集和心跳上报。
- 明确 Ace Agent 与用于远程控制的 MeshCentral Agent 不是同一个客户端。
- 从下载视图直接进入现有设备接入令牌流程。
- 保持现有移动端、深色模式和功能导向的视觉体系。

## 非目标

- 不新增公开的未登录下载页。
- 不修改 Backend、数据库、Agent 二进制或 `/downloads/` 静态文件服务。
- 不在本次改动中提供 Windows 安装包、系统服务注册或 Linux 包管理器安装。
- 不引入 Vue Router，也不增加浏览器历史记录或可分享的子路由。
- 不实现 MeshCentral Agent 下载或安装。

## 交互设计

### 导航

`OperationsWorkspace.vue` 增加轻量的本地视图状态，支持：

- `overview`：现有设备总览、组织结构和设备表格。
- `downloads`：新的客户端下载视图。

侧边栏增加带下载图标的“客户端下载”入口。点击导航项后切换视图，并在移动端关闭侧边栏。活动导航项根据当前视图显示，避免“设备总览”在下载页仍保持选中。

“组织结构”继续属于 `overview`。从下载页点击它时，先切回 `overview`，再滚动到组织结构区域。现有“设备接入”入口继续打开 Enrollment Token 弹窗。

### 下载视图

下载视图复用现有工作台框架和顶部操作区，不创建嵌套卡片或营销式页面。顶部标题为“客户端下载”，正文使用两行结构化平台条目：

1. Windows x64
   - 文件：`AceAgent-windows-amd64.exe`
   - 适用平台标识：Windows / x64
   - 下载地址：`/downloads/AceAgent-windows-amd64.exe`
2. Linux x64
   - 文件：`ace-agent-linux-amd64`
   - 适用平台标识：Linux / x64
   - 下载地址：`/downloads/ace-agent-linux-amd64`

下载链接使用相对路径，由浏览器自动继承当前 Origin。因此页面通过 `http://it.ace-station.top:1111` 或其他部署域名打开时，无需修改前端配置。

每个平台条目使用平台图标、名称、架构信息、文件名和带下载图标的明确下载按钮。按钮使用原生 `<a download>`，保留浏览器标准下载行为和键盘可访问性。

### 平台说明与接入操作

平台列表下方显示两项简短说明：

- Ace Agent：设备注册、基础资产采集、资源状态和心跳上报。
- MeshCentral Agent：远程桌面、终端和文件操作，属于独立客户端，后续接入。

页面提供“生成接入令牌”主操作按钮。点击后通过组件事件通知 `OperationsWorkspace.vue`，复用现有 `openDialog('enrollment')`，不重复实现令牌表单或 API 调用。

当尚未创建分组时，该按钮禁用，与现有“添加设备”按钮规则一致。

## 组件边界

新增 `frontend/src/components/AgentDownloads.vue`：

- 只负责下载视图的静态内容和用户操作。
- 不访问 API，不持有组织或节点数据。
- 接收 `canEnroll: boolean`，用于控制接入按钮状态。
- 发出 `enroll` 事件，请求父组件打开现有设备接入流程。

修改 `frontend/src/components/OperationsWorkspace.vue`：

- 管理 `activeView`。
- 渲染正确的导航活动状态。
- 在 `downloads` 视图中渲染 `AgentDownloads`。
- 处理移动端导航关闭和 `enroll` 事件。
- 保留现有数据加载、主题切换、Enrollment Token 生成和节点展示逻辑。

该边界让下载页保持独立可测，同时避免把 API 和弹窗状态复制到新组件。

## 视觉与响应式规则

- 延续现有 IBM Plex Sans、IBM Plex Mono、4 px 圆角和蓝色功能色。
- 页面采用紧凑的运维工作台布局，以边框、分隔线和对齐表达层级，不增加装饰性渐变或大面积插图。
- 平台条目在移动端单列显示，下载按钮保持至少 44 px 高。
- 桌面端平台信息使用稳定网格对齐，文件名可换行或截断，不挤压下载操作。
- 所有新增颜色使用现有 CSS 变量，同时支持浅色和深色主题。
- 不新增装饰动画；现有 `prefers-reduced-motion` 规则继续生效。

## 错误处理

下载文件由现有 Nginx 静态服务提供，页面不预先发请求，不引入额外加载状态。文件不存在或网络不可用时，由浏览器展示标准下载失败行为。

Enrollment Token 流程继续使用现有弹窗错误提示。下载组件不捕获或改写该错误。

## 测试方案

遵循 Red-Green-Refactor：

1. 先新增 `AgentDownloads.test.ts`，验证 Windows 和 Linux 平台信息及两个准确下载地址，确认测试因组件不存在而失败。
2. 验证“生成接入令牌”按钮在可用时发出 `enroll` 事件，在无分组时禁用。
3. 为 `OperationsWorkspace` 增加聚焦导航行为的测试，验证点击“客户端下载”后显示下载视图并更新活动导航项。
4. 实现最小组件和视图状态，使新增测试通过。
5. 运行完整 Frontend 测试和生产构建。

## 验收标准

- 登录后侧边栏可进入“客户端下载”。
- 页面准确展示 Windows x64 和 Linux x64。
- 两个下载按钮分别指向现有同源下载文件。
- 页面明确说明 Ace Agent 与 MeshCentral Agent 的职责差异。
- 有分组时可从页面打开设备接入弹窗，无分组时按钮禁用。
- 返回设备总览后，原有指标、组织结构和设备列表正常显示。
- 移动端导航在选择下载页后关闭，无横向溢出或文本遮挡。
- 浅色和深色主题均保持可读。
- 全部 Frontend 测试和生产构建通过。
