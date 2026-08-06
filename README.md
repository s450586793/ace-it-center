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

脚本只从 GHCR 拉取已经通过 CI 的镜像；DSM 不拉取源码，也不在本机编译。源码仓库保持 Private，
`backend`、`web` 和 `updater` 三个 GHCR package 发布为 Public，因此 DSM 不需要执行
`docker login` 或保存 GHCR Token。首次部署、不可变 updater 标签和 Owner Web 升级的运维步骤见
[`deploy/README.md`](deploy/README.md)。

## 验证

```bash
go test ./...
cd frontend && npm ci && npm test -- --run && npm run build
```

向 `main` 推送代码时，GitHub Actions 会运行测试并发布 backend/web 的 `latest` 与 `sha-*` 镜像；
updater 在 main 只构建验证，不推送任何 mutable 标签。只有有效的 `vX.Y.Z` release tag 才会发布
同名的 updater 不可变版本；正式发布的 `stable` promotion 仍只包含 backend/web。
