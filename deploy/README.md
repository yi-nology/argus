# deploy/ 部署物索引

| 目录 / 文件 | 形态 | 说明 |
|---|---|---|
| [`docker-compose/`](docker-compose/) | 单机 | compose + plugins + `.env.example`；SQLite 落 `./data` |
| [`helm/argus/`](helm/argus/) | K8s HA | 双副本 + PostgreSQL + Migration Job + 备份 Cron |
| [`k8s/`](k8s/) | 裸 K8s | 00-namespace → 06-service + ingress/ocrshim 示例 |
| [`schema.sql`](schema.sql) | MySQL 8.0 | 规范 schema（应用 AutoMigrate 兜底） |
| [`github-app-manifest.yaml`](github-app-manifest.yaml) | GitHub App | 权限与 webhook 清单 |

指引见 [docs/DEPLOY.md](../docs/DEPLOY.md)。
