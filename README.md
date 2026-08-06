# Ace IT Center

Ace IT Center 是面向个人和小企业的私有化 IT 基础设施管理平台。当前版本包含设备接入、状态采集、分组与备注、网络流量统计、Windows Agent 自动更新和远程命令任务。

## 仓库结构

- `backend/`、`internal/`：Go 控制平面和 PostgreSQL 存储；
- `frontend/`：Vue 3 管理界面；
- `agent/`：Windows 和 Linux Agent；
- `installer/`：Windows 安装器；
- `deploy/`：DSM Compose、Nginx 和 Windows builder；
- `scripts/`：构建、发布和 DSM 更新脚本。

## DSM 部署

生产数据、Release、`.env` 和签名私钥只保存在 DSM，不进入 Git。完整部署说明见 [`deploy/README.md`](deploy/README.md)。

更新 DSM Compose 项目：

```bash
cd /volume4/docker/docker/ace-it-center
bash scripts/deploy-dsm.sh
```

脚本只从 GHCR 拉取已经通过 CI 的镜像；DSM 不拉取源码，也不在本机编译。

## 验证

```bash
go test ./...
cd frontend && npm ci && npm test -- --run && npm run build
```

向 `main` 推送代码时，GitHub Actions 会运行测试，并将 `backend`、`web` 镜像发布到 GHCR。
