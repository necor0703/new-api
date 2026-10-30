# New-API 源码生产部署文档（necor0703 fork）

> 源码：https://github.com/necor0703/new-api.git（含模型分析页 group 筛选二开）
> 部署包位置（仓库内）：`deploy/` —— 一键脚本 + 两套 compose + 配置模板

---

## 0. 一键生产部署（新机器看这节就够）

```bash
# 1) 拉仓库（仓库里自带 deploy/）
git clone https://github.com/necor0703/new-api.git /root/newapi-src-prod
cd /root/newapi-src-prod

# 2) 准备配置（只改这一份）
cp deploy/.env.example /app/new-api-prod/.env
vim /app/new-api-prod/.env   # 必改：GIT_REF / APP_PORT / MW_MODE / 密码类

# 3) 一键部署（git拉码→docker构建→装/复用PG+Redis→compose起服→健康检查）
./deploy/deploy.sh /app/new-api-prod/.env
```

脚本做完后会打印访问地址；首次使用需初始化管理员：

```bash
curl -X POST http://127.0.0.1:<端口>/api/user/setup \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<密码>","confirmPassword":"<密码>"}'
```

### `.env` 关键项

| 项 | 说明 |
|---|---|
| `GIT_REPO` / `GIT_REF` | 源码地址与版本；生产建议 pin 一个 commit 或 tag，而不是 main |
| `APP_PORT` | 对外端口，默认 3000 |
| `MW_MODE=host` | 复用本机已有 PG/Redis（默认）。没有就 apt 装，有就直接用；自动建账号建库、放行容器网段、密码随机生成写回 `.env` |
| `MW_MODE=bundled` | 全新空机用：compose 顺手起 `postgres:15` + `redis:7-alpine` 两个容器，数据落 `pg_data` / `redis_data` 卷 |
| `PG_DB` / `PG_LOG_DB` / `PG_USER` | host 模式下的业务库 / 日志库 / 账号（脚本自动创建，默认 newapi / newapi_log / newapi） |
| `REDIS_PASSWORD` / `REDIS_DB` | host 模式本机 Redis 密码与库号（避免和别的业务串库） |
| `POSTGRES_PASSWORD` / `REDIS_PASSWORD_BUNDLED` | bundled 模式两容器的密码，空则脚本随机生成写回 `.env` |
| `SESSION_SECRET` | 空则脚本随机生成 64 位 hex 写回 `.env`，多节点必须一致 |
| `SQL_DSN` / `LOG_SQL_DSN` / `REDIS_CONN_STRING` | 由脚本组装写回，不要手填 |

### deploy/ 文件清单

```
deploy/
├── deploy.sh                 # 一键部署脚本（git→build→中间件→compose→健康检查）
├── docker-compose.yml         # host 模式：只起 new-api，连本机 PG/Redis
├── docker-compose.bundled.yml # bundled 模式：new-api + postgres + redis 三容器
└── .env.example               # 配置模板（复制为 /app/new-api-prod/.env 后改）
```

> 注意：不要把填好密码的 `.env` 提交进仓库（`.gitignore` 已忽略 `.env`），只提交 `.env.example`。

---

## 1. 本机部署现状（2026-10-30，Oracle ARM aarch64 24G）

| 实例 | 来源 | 端口 | 数据库 | Redis | 状态 |
|---|---|---|---|---|---|
| `new-api`（旧） | `calciumion/new-api:latest` 镜像 | 3002 | sqlite `/app/new-api/data/one-api.db` | 未接 | 已下线 |
| `new-api-src`（当前） | fork 源码本地构建 `new-api:src-group-filter` | 3002 | 本机 PG16 `newapi` + `newapi_log` | 本机 Redis db1 | 运行中，已验证中转 |

> 2026-10-30 18:15 已下掉旧镜像实例，`new-api-src` 从 3003 切换到 3002。
> 旧数据备份：`/root/newapi-migrate/new-api-old-3002-backup-20261030.tar.gz`（sqlite 库+日志+compose），`/app/new-api/` 目录保留未删。

访问：http://144.24.33.39:3002（admin / 原密码不变）

### 部署目录

```
/app/new-api-src/
├── docker-compose.yml   # 容器编排（600 权限，含密码）
├── data/                # SQLite 兜底 / 本地文件（PG 为主库时基本为空）
└── logs/                # 容器日志卷
/root/new-api-src/       # git clone 的源码（含 Dockerfile）
/root/newapi-migrate/
├── newapi-config-full.json  # 旧库全量导出（channels/options/abilities/tokens/users）
├── .pgpass_src              # PG newapi 账号密码（600）
├── .admin_pass_src          # setup 阶段临时密码（已失效，admin 已恢复为旧密码）
├── pg_hba.conf.bak / postgresql.conf.bak / redis.conf.bak  # 改前备份
```

### 中间件（复用本机现有，未新装）

- **PostgreSQL 16**（本机已有，与 sub2api 共实例）：
  - 新建 `ROLE newapi`、`DATABASE newapi`（业务库）、`DATABASE newapi_log`（日志库，`LOG_SQL_DSN`）。
  - 改动：`postgresql.conf` → `listen_addresses='127.0.0.1,172.17.0.1'`（改 listen 要 restart，reload 不生效）；
    `pg_hba.conf` 放行 `172.16.0.0/12` + `10.0.0.0/8`（compose 自建网段容器源 IP 不固定，只放 docker0 /32 会 28000 连不上）。
  - sub2api 走 127.0.0.1 本地连接，不受影响。
- **Redis**（本机已有，有密码）：
  - 改动：`redis.conf` 的 `bind` 加 `172.17.0.1` 并重启（本地连接不受影响）。
  - 新实例用 **db1**（旧实例没接 Redis；db0 有 1592 个现有 key，未动）。

### 构建（约 3 分钟，ARM 本地编译）

```bash
git clone https://github.com/necor0703/new-api.git /root/newapi-src
cd /root/newapi-src
docker build -t new-api:src-$(git rev-parse --short HEAD) .
```

三阶段：bun 前端 → golang 1.26 后端 → debian-slim 运行镜像（~340MB）。
注意：本机无 go/bun 环境，构建一律走 `docker build`（宿主机 `go` 命令不可用）。

### 启动与初始化

```bash
cd /app/new-api-src && docker compose up -d
# 首次启动自动 gorm AutoMigrate 建表（37 张），Redis 连上显示 "Redis is enabled"
# 初始化管理员：
curl -X POST http://localhost:3003/api/user/setup \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"xxx","confirmPassword":"xxx"}'
```

### 配置迁移（sqlite → PG，已完成）

| 表 | 行数 | 说明 |
|---|---|---|
| channels | 3 | 密钥逐字节一致（sha 对过）；4 智谱GLM / 5 智谱GLM_1（vip）、6 额度用完（default） |
| options | 31 | 全量；仅 `ServerAddress`/`TaskPublicAddress` 改为 `:3003` |
| abilities | 8 | 分组×模型×渠道映射 |
| users | 3 | admin 保留旧密码+旧余额（quota 99598904）；test、JulianMoore542 密码哈希原样迁移（同 argon2id，可直接登录） |
| tokens | 4 | key 原样迁移；id=3 那条在旧库已软删除（deleted_at），同样迁入 |

注意：Postgres 布尔列（abilities.enabled / tokens.unlimited_quota 等）迁移时做了 int→bool 转换。

### 验证结果（2026-10-30）

- `/api/status` 正常；登录、渠道/用户/令牌/配置接口正常。
- 真实中转测试：同一把 `code(auto)` key，旧实例(3002)与新实例(3003)调 `glm-5.3-flash` 均返回 `ok`。
- 分组路由行为一致：`group=test` 的 key 在新旧实例上同样报无可用渠道（test 分组本来就没配渠道）。

---

## 2. 日常运维

```bash
cd /app/new-api-src
docker compose ps            # 看状态
docker compose logs -f       # 看日志
docker compose restart       # 重启
docker compose down          # 停服（数据在 PG/Redis 里，不丢）

# 改配置后重建
docker compose up -d --force-recreate

# 从 fork 更新
cd /root/newapi-src && git pull
docker build -t new-api:src-$(git rev-parse --short HEAD) .
# 改 docker-compose.yml 里 image 后 up -d
```

敏感信息位置：`/app/new-api-src/docker-compose.yml`（PG/Redis 密码、SESSION_SECRET）、
`/root/newapi-migrate/.pgpass_src`、`cat /app/new-api/.admin_pass`（admin 密码，新旧实例通用）。

## 3. 后续二开流程

```bash
cd /root/newapi-src
# 改代码 → docker build -t new-api:src-xxx . → 改 compose image → up -d
# 前端改动：容器内已含构建产物，无需 node 环境；后端改动需重编 Go（约 2-3 分钟）
```

## 4. 二开记录（2026-10-30，commit 2e79958，已推 fork main）

模型调用分析页新增按分组筛选（Filter 弹窗 → Group Filter → Group 下拉）：
- 后端：`GET /api/data/`、`/api/data/self` 支持 `group` 查询参数（`controller/usedata.go` + `model/usedata.go`，空=不过滤）
- 前端：`DashboardFilters.group` + Filter 弹窗分组下拉（选项走 `/api/user/self/groups`），7 文件 +99/-14
- 重新构建 `new-api:src-group-filter` 并切到 3002，对应 compose image 已更新
