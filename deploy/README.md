# Ace IT Center DSM 部署

当前 Compose 部署包含 3 个服务：

- `web`：Vue 3 前端、Agent 下载和 `/api` 反向代理；
- `backend`：Go 控制平面；
- `postgres`：平台持久数据。

## DSM 前置条件

- DSM 7.2；
- Container Manager；
- Docker Compose v2；
- 数据目录 `/volume3/work/ace-it-center`；
- 空闲 TCP 端口 `9060`。

## 部署

在 `deploy` 目录创建本地环境文件：

```bash
mkdir -p /volume3/work/ace-it-center/postgres
cp .env.example .env
chmod 600 .env
```

必须将 `POSTGRES_PASSWORD` 替换为仅包含字母和数字的高强度随机值，避免数据库连接 URL 转义问题。然后执行：

```bash
sudo docker compose build
sudo docker compose up -d
sudo docker compose ps
```

默认使用 `goproxy.cn` 和 `registry.npmmirror.com` 下载构建依赖，可通过 `.env` 中的 `GOPROXY`、`NPM_REGISTRY` 覆盖。

健康检查：

```bash
curl http://127.0.0.1:9060/api/v1/health
```

首次打开 `http://DSM-IP:9060` 时，页面会要求创建平台 Owner 账户。

## Agent 下载

- Linux amd64：`http://DSM-IP:9060/downloads/ace-agent-linux-amd64`
- Windows amd64：`http://DSM-IP:9060/downloads/AceAgent-windows-amd64.exe`

创建组织、站点、分组和一次性 Enrollment Token 后，可执行一次注册与心跳：

```bash
sudo install -m 0755 ace-agent-linux-amd64 /usr/local/bin/ace-agent
sudo ace-agent -server http://DSM-IP:9060 -enrollment TOKEN -once
```

## HTTPS 切换

当前 `ACE_SECURE_COOKIES=false` 仅适用于 DSM 内网 HTTP 验证。Cloudflare Tunnel 或 DSM 反向代理提供 HTTPS 后，将其改为 `true` 并重建 Backend 容器：

```bash
sudo docker compose up -d --force-recreate backend
```

## 数据与升级

PostgreSQL 数据保存在 `/volume3/work/ace-it-center/postgres`。升级前先备份该目录，然后在项目目录重新执行：

```bash
sudo docker compose build --pull
sudo docker compose up -d
```
