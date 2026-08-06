# Ace IT Center DSM 部署

当前 Compose 部署包含 4 个服务：

- `web`：Vue 3 前端、持久化 Agent release 下载和 `/api` 反向代理；
- `backend`：Go 控制平面；
- `postgres`：平台持久数据，不参与 Web 升级；
- `updater`：仅在内部网络运行的 Web 升级执行器；它不发布 DSM 端口，且不通过 Web 自我升级。

## DSM 前置条件

- DSM 7.2；
- Container Manager；
- Docker Compose v2；
- 数据目录 `/volume4/docker/docker/ace-it-center`；
- 空闲 TCP 端口 `9060`。

## 部署

项目根目录的 `compose.yaml` 是 DSM Container Manager 项目的唯一入口。创建项目时选择目录
`/volume4/docker/docker/ace-it-center` 和文件 `compose.yaml`；不要再从 `deploy/compose.yaml` 启动服务。

在项目根目录创建本地环境文件：

```bash
cd /volume4/docker/docker/ace-it-center
mkdir -p postgres releases/windows updater-state backups
cp .env.example .env
chmod 600 .env
```

必须将 `POSTGRES_PASSWORD` 替换为仅包含字母和数字的高强度随机值，避免数据库连接 URL 转义问题。
还必须生成至少 32 随机字节的 `ACE_UPDATER_TOKEN`，该 Token 只保存在 DSM 的 `.env`，其权限必须为
`0600`，不得写入 Git、日志或浏览器。例如：

```bash
openssl rand -base64 48
```

将结果写入 `ACE_UPDATER_TOKEN` 后，把 `ACE_UPDATER_IMAGE_TAG` 设置为不可变的 `vX.Y.Z`，例如
`v0.4.1`。然后启动四服务 Compose 项目：

```bash
sudo docker compose config
sudo docker compose pull
sudo docker compose up -d --no-build
sudo docker compose ps
```

## GitHub 镜像部署

源码推送到 GitHub 后，GitHub Actions 自动测试并构建 GHCR 镜像。DSM 的 Compose 项目只拉取镜像，不拉取源码，也不执行本地构建。更新时执行：

```bash
cd /volume4/docker/docker/ace-it-center
bash scripts/deploy-dsm.sh
```

源码仓库保持 Private。首次发布后，在 GitHub Package settings 中将 `backend`、`web` 和 `updater`
三个 GHCR package 都设为 Public。DSM 首次部署及后续拉取都不需要 `docker login`，也不应在 DSM
保存 GitHub 或 GHCR Token。

在 `.env` 中配置 Backend/Web 的 stable 标签和 updater 的不可变标签：

```dotenv
ACE_IMAGE_TAG=stable
ACE_UPDATER_IMAGE_TAG=vX.Y.Z
```

`ACE_IMAGE_TAG=stable` 仅供 backend/web 的 Owner 页面升级流程发现与切换。`updater` 永远使用显式的
不可变 `vX.Y.Z`；更新 updater 本身时，只能在 DSM Container Manager 中手动更新项目，或显式修改
`.env` 中的 tag 后手动执行 Compose recreation。Web 升级绝不更新 `postgres` 或 `updater`。

Public 镜像的 filesystem layer 可被任何人下载和检查。构建上下文和镜像 layer 中绝不能包含 `.env`、
Token、数据库、签名私钥或任何业务数据。

新版本健康检查通过后，部署脚本会运行 `scripts/cleanup-dsm-images.sh`。该脚本只处理 Ace IT Center、Windows builder 和 Go 测试镜像，并保留所有仍被容器引用的镜像；不会清理其他 DSM 项目。

健康检查：

```bash
curl http://127.0.0.1:9060/api/v1/health
```

首次打开 `http://DSM-IP:9060` 时，页面会要求创建平台 Owner 账户。

## Agent 下载

- Linux amd64：`http://DSM-IP:9060/downloads/ace-agent-linux-amd64`
- Windows amd64 stable installer：`http://DSM-IP:9060/downloads/windows/stable/AceAgentSetup-windows-amd64.exe`

### Windows 首次接入（免 Token）

1. 下载并安装 Windows stable installer。
2. 从托盘打开 Ace Agent，只填写 Ace IT Center 服务器地址，例如 `http://DSM-IP:9060`。
3. 在网页的“待配对设备”中选择目标分组并批准请求。
4. 确认设备状态变为在线并开始正常心跳。

Windows 图形化首次接入不需要、也不应输入 Enrollment Token。Token CLI 仅为现有 Linux 设备接入和自动化脚本兼容保留：

```bash
sudo install -m 0755 ace-agent-linux-amd64 /usr/local/bin/ace-agent
sudo ace-agent -server http://DSM-IP:9060 -enrollment TOKEN -once
```

Windows Agent 在服务启动时立即检查稳定版更新，之后每 `1 小时 + 0～10 分钟随机抖动`检查一次，并静默安装通过签名验证的新版本。

## 构建并发布 Windows release

Windows builder 是一次性容器。它固定 Go、Inno Setup 和 innoextract 输入，并基于 Debian Bookworm 构建；Wine 由 Bookworm 软件源在 image build 时安装。在容器运行时，它从只读 mount 读取 Ed25519 private key；private key 不会进入 image layer、release artifact 或构建日志。先在受控主机上生成密钥，并只将 private key 放到 DSM secret 目录：

```bash
go run ./tools/cmd/ace-release keygen \
  -private /volume4/docker/docker/ace-it-center/secrets/update-signing.key \
  -public /volume4/docker/docker/ace-it-center/secrets/update-signing.key.pub
chmod 600 /volume4/docker/docker/ace-it-center/secrets/update-signing.key
```

每次发布都必须提供 SemVer release version 和当前 Git revision。可选时间参数必须是 whole-second UTC RFC3339；未提供时 builder 使用当前 UTC 时间：

```bash
export RELEASE_VERSION=0.2.0
export RELEASE_COMMIT="$(git rev-parse --short=12 HEAD)"
export RELEASE_BUILT_AT="$(date -u +%FT%TZ)"
sudo --preserve-env=RELEASE_VERSION,RELEASE_COMMIT,RELEASE_BUILT_AT \
  docker compose -f deploy/windows-builder.compose.yaml build --pull
sudo --preserve-env=RELEASE_VERSION,RELEASE_COMMIT,RELEASE_BUILT_AT \
  docker compose -f deploy/windows-builder.compose.yaml run --rm windows-builder
```

builder 会从只读 private key 派生 public key 并编译进 Agent，使用真实 ISCC 生成 installer，检查 installer inventory，签名并验证 `latest.json`，最后原子发布到 `${ACE_RELEASES_DIR}/windows/stable`。它拒绝 downgrade 和覆盖同版本目录。成功后不存在运行中的 builder container；builder image 和 BuildKit cache 可删除，持久 release 不受影响。

Web 以只读方式挂载 `${ACE_RELEASES_DIR}/windows`。versioned installer 使用 immutable cache，stable alias 和 `latest.json` 使用 no-cache；发布新 release 不需要重建 Web image。

## HTTPS 切换

当前 `ACE_SECURE_COOKIES=false` 仅适用于 DSM 内网 HTTP 验证。Cloudflare Tunnel 或 DSM 反向代理提供 HTTPS 后，将其改为 `true` 并重建 Backend 容器：

```bash
sudo docker compose up -d --force-recreate backend
```

## 数据与升级

PostgreSQL 数据保存在 `${ACE_DATA_DIR}/postgres`，Windows Agent Release 保存在
`${ACE_RELEASES_DIR}`。Web 升级前由 updater 创建 PostgreSQL custom-format 备份，路径为
`${ACE_DATA_DIR}/backups`，文件名形如 `upgrade-<UTC>-<task-id>.dump`。这不是 DSM 灾难恢复备份的替代品；
仍应按既有运维制度备份数据目录和 release。

Backend/Web 的稳定版升级应由 Owner 页面“系统升级”发起：先检查最新 stable，再确认准确目标版本。
不要用 `deploy-dsm.sh` 代替该事务来重建 backend/web。普通部署或手动更新 updater 时，才在项目根目录执行：

```bash
bash scripts/deploy-dsm.sh
```

升级成功但页面显示 `cleanup_pending` 时，先检查镜像引用；确认后只能删除页面显示的 Ace IT Center
旧 backend/web 镜像，不能使用 prune、强制删除或按名称模糊删除。

页面显示 `manual_intervention` 时：停止 updater，保留 `updater-state/update-state.json` 和全部新旧镜像；
检查 backend/web，并先将两者恢复到同一个版本；随后归档受阻的状态文件，再重启 updater。不要删除
PostgreSQL、updater、Windows builder 或其他 Compose 项目的镜像。

首次上线此功能时，只有在证明没有任何容器引用后，才可一次性清理遗留的 `pre-v*` backend/web 镜像。
该清理明确排除 postgres、updater、Windows builder 和其他项目；后续升级不重复执行此遗留清理。

## 维护记录

每次 Windows release 发布后，记录非敏感的版本、UTC 时间、Compose 容器状态和已完成的验收范围；不要记录服务器凭据、会话 Cookie、Token 或配对密钥。真实 Windows 安装、托盘交互及恢复接入必须在受控 Windows 测试机上单独验收。

- 2026-07-30 UTC：发布 Windows Agent `0.3.2`。`postgres`、`backend`、`web` 均 healthy；公开 health、stable manifest 与未登录 pairing 管理接口 smoke 已通过。DSM 验证时间 `2026-07-30T18:24:35Z`：backend `sha256:e886be694b34760a38c18be050e04da84840e56560c87699d5068f20e3de79dc`，web `sha256:60470d1f8d24a8b4d2ab29759f93868d823a6d2b9a65757a0e23a9a176b328c6`。已完成 Go 全量测试、前端 47 项测试、前端生产构建和 Windows 构建脚本测试。真实 Windows 安装、网页批准、恢复接入、旧凭据拒绝和过期流程仍须在受控 Windows 测试机执行。
- 2026-07-30 UTC：发布 Windows Agent `0.3.3`，版本化安装包为 `AceAgentSetup-windows-amd64-V0.3.3.exe`。DSM 验证时间 `2026-07-30T18:50:55Z`：backend `sha256:e886be694b34760a38c18be050e04da84840e56560c87699d5068f20e3de79dc`，web `sha256:e00eb249250adf41f328f0b44fce16623d9f61f325d296bf043c79af46339ffb`。`postgres`、`backend`、`web` 均 healthy；公开 health、stable manifest、安装包下载和 SHA-256/大小 smoke 已通过，builder 容器已按 `--rm` 清理。真实 Windows 安装、网页批准、恢复接入、旧凭据拒绝和过期流程仍须在受控 Windows 测试机执行。
- 2026-07-31 UTC：发布 Windows Agent `0.3.4`，修复多地址设备优先上报 IPv6 的问题，版本化安装包为 `AceAgentSetup-windows-amd64-V0.3.4.exe`。DSM 验证时间 `2026-07-31T16:40:17Z`：backend `sha256:b988e11ea99d69dfbc558a8fc94e5dec68f55a1ed2d0d325fdcfec45f8edf7d9`，web `sha256:55eef8afea1d3ebff55d6d327e4fdf08f6e2d66423fdc620fa6c02b65348a4d7`。`postgres`、`backend`、`web` 均 healthy；Go 全量测试、前端 57 项测试、前端生产构建、Windows Agent 25 项构建契约测试、Windows release 11 项发布契约测试、公开 health、stable manifest、安装包下载及 SHA-256 验证均已通过。既有 `0.3.3` 客户端的定时静默升级和升级后 IPv4 心跳仍需等待客户端更新周期完成后观察。
- 2026-07-31 UTC：发布 Windows Agent `0.3.5`，将服务启动后的自动更新检查周期调整为每 `1 小时 + 0～10 分钟随机抖动`，版本化安装包为 `AceAgentSetup-windows-amd64-V0.3.5.exe`。DSM 验证时间 `2026-07-31T17:44:55Z`：Windows builder `sha256:9cb6e2f51397a0a8815a2d511a5d40a86b431ab6bd9704ee5f7b0b6d38a1a6fe`，backend `sha256:b988e11ea99d69dfbc558a8fc94e5dec68f55a1ed2d0d325fdcfec45f8edf7d9`，web `sha256:a5c00f1fe2f090e29d20964a51b4a814f6b1b766bfa4eb842f17368bcf222f09`。`postgres`、`backend`、`web` 均 healthy；Go 全量测试、前端 57 项测试、前端生产构建、Windows Agent 25 项构建契约测试、Windows release 11 项发布契约测试、公开 health、stable manifest、安装包下载及 SHA-256/大小验证均已通过，builder 容器已按 `--rm` 清理。既有 `0.3.3` 客户端首次发现本版本仍受旧的 6 小时周期约束，升级后才采用新周期。
- 2026-08-03 UTC：发布 Windows Agent `0.4.0` 和首版 Windows 命令中心，release metadata commit 为 `aa88be7f712c25fa83063a8bdee693b0e3bef572`，版本化安装包为 `AceAgentSetup-windows-amd64-V0.4.0.exe`，大小 `4902087` bytes，SHA-256 为 `81b664e9103b4859fd8deaaece30245c756f708b52be8f5ca08d748b8a7815b3`。DSM 验证时间 `2026-08-03T14:05:13Z`：backend `sha256:6976b8e47481de7485a5b74dfc2aebf380038448b55d94bbe0df164a74d261d9`，web `sha256:07081db91fc6e816321a3496874623a69ada8d6346125ac70d679bfd1b914a49`；`postgres`、`backend`、`web` 均 healthy，`command_tasks` 和 `command_executions` 迁移已创建。Go 全量测试、前端 `69` 项测试、前端生产构建、Windows amd64 交叉编译、Windows Agent `25` 项构建契约、Windows release `11` 项发布契约、公开 health、命令 API 未登录保护、stable manifest、安装包下载及 SHA-256/大小验证均已通过，builder 容器已按 `--rm` 清理。真实 Windows 成功、非零退出、超时终止、离线排队和命令期间心跳验收需在安装 `0.4.0` 后执行；自动更新可靠性保留到真实 `0.4.0 -> 0.4.1` 升级验证。
