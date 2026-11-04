# new-api 新服务器从零部署（bundled 单文档版，Ubuntu 24.04，fork @ 4bd309e）

> 目标读者：全新云服务器，什么都没装。SSH 以 root 连上后，照本文从头到尾复制执行即可。
> 本文档只讲 **bundled 模式**：compose 顺手起 `new-api + postgres:15 + redis:7-alpine` 三个容器，
> PG/Redis 都在容器里，不碰系统软件，新机无负担。
> 全程 root 执行。预计耗时：环境约 10 分钟 + 构建约 3～8 分钟。

---

## 第 0 步：确认机器 + 放行端口

```bash
whoami
# 期待输出 root。如不是，先切 root：sudo -i

cat /etc/os-release | head -n 3
# 期待 Ubuntu 22.04 / 24.04（Debian 11/12 亦可，命令通用）

uname -m
# x86_64 或 aarch64 均可，Dockerfile 已做多架构（TARGETARCH 自适应）

# 云安全组先放行 TCP 端口（默认 3000，按你 .env 里 APP_PORT 为准）
# 去云控制台：新机实例 → 安全组 → 入方向 → 放行 TCP 3000（来源 0.0.0.0/0，或先限你公司 IP）
```

---

## 第 1 步：装基础工具（git/curl/常用，一条命令）

```bash
apt-get update && apt-get install -y git curl wget ca-certificates openssl python3 sudo

# 验证
git --version
curl --version | head -n 1
python3 --version
```

说明：`deploy.sh` 需要 `git` 拉源码、`curl` 做健康检查、`openssl` 生成随机密码、`python3` 组装 DSN。

---

## 第 2 步：装 Docker（含 compose 插件，新机必装，全复制）

```bash
# 1) 卸载可能残留的旧 docker（全新机器一般没有，有也不报错）
for p in docker.io docker-doc docker-compose docker-compose-v2 podman-docker containerd runc; do apt-get remove -y $p 2>/dev/null || true; done

# 2) 配 Docker 官方源
apt-get install -y ca-certificates curl
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" > /etc/apt/sources.list.d/docker.list
apt-get update

# 3) 装 docker 全家桶（含 compose 插件）
apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

# 4) 开机自启 + 立即启动
systemctl enable --now docker

# 5) 验证（两行都有版本号才算成）
docker --version
docker compose version
```

---

## 第 3 步：bundled 模式说明（先看懂再动手，30 秒）

```
┌────────────── 宿主机（只装 docker） ──────────────┐
│  new-api-prod          :3000 → 3000               │
│  new-api-prod-postgres  postgres:15，库 new-api   │  数据 → pg_data 卷
│  new-api-prod-redis     redis:7，0 号库           │  数据 → redis_data 卷
│  三者同 new-api-network 网桥，服务名互通           │
└──────────────────────────────────────────────────┘
```

- 用户/库名写死：PG 用户 `postgres`、库 `new-api`；Redis 用 `0` 号库。你只定两个密码。
- `LOG_SQL_DSN` 为空是正常的：bundled 故意复用主库存日志（`model/main.go InitLogDB` 留空=复用，支持）。
- 数据持久化靠 `pg_data` / `redis_data` 两个命名卷，`docker compose down` 不丢数据（`down -v` 才会删卷，别手滑）。

---

## 第 4 步：拉仓库 + 写配置（全文只改这一份 `.env`）

```bash
# 1) 拉 fork 源码（仓库里自带 deploy/）
git clone https://github.com/necor0703/new-api.git /root/newapi-src-prod
cd /root/newapi-src-prod
git rev-parse --short HEAD   # 记下短 SHA，应为 4bd309e 或之后的新 commit

# 2) 准备配置
mkdir -p /app/new-api-prod
cp deploy/.env.example /app/new-api-prod/.env
chmod 600 /app/new-api-prod/.env
vim /app/new-api-prod/.env
```

`.env` 按 bundled 这样填（`vim` 里按 `i` 编辑，`Esc` 后 `:wq` 保存）：

```ini
# --- 必看三项 ---
GIT_REF=29e7401                  # 别改 main！生产 pin 已验证 commit
APP_PORT=3000                    # 对外端口；改了它，第 0 步安全组也要改
MW_MODE=bundled                  # 本文档只用 bundled

# --- bundled 密码（二选一：自定 或 留空让脚本随机） ---
POSTGRES_PASSWORD=PgXx2026_NewApi_Strong01
REDIS_PASSWORD_BUNDLED=RsXx2026_NewApi_Strong02
# 想让脚本随机生成就把上面两行等号右边清空，部署完脚本会写回 .env

# --- 以下不要手填，脚本组装 ---
# SQL_DSN=
# LOG_SQL_DSN=                   # bundled 留空=复用主库，正常
# REDIS_CONN_STRING=
# SESSION_SECRET=                # 留空=随机生成；多节点必须各节点填一样
```

密码要求：16～32 位字母+数字最稳。特殊字符脚本能处理（URL 编码），但手敲/复制容易错，第一次部署建议纯字母数字。

填完自检（复制执行）：

```bash
grep -E '^(APP_PORT|MW_MODE|GIT_REF|POSTGRES_PASSWORD|REDIS_PASSWORD_BUNDLED)=' /app/new-api-prod/.env
ss -ltnp | grep ":$(grep '^APP_PORT=' /app/new-api-prod/.env | cut -d= -f2)" || echo "端口空闲，OK"
```

---

## 第 5 步：一键部署（一条命令，约 3～8 分钟）

```bash
cd /root/newapi-src-prod
./deploy/deploy.sh /app/new-api-prod/.env
```

脚本内部做的事（不用你动手，出错对照看）：
1. `git checkout GIT_REF`（pin 住就是 29e7401）
2. `docker build -t IMAGE_TAG`（三阶段：bun 前端 → golang:1.26 后端 → debian-slim，约 340MB）
3. 为空的密码 `openssl rand -hex 16` 生成并写回 `.env`
4. 组装容器内 DSN：`postgres://postgres:***@postgres:5432/new-api`、`redis://:***@redis:6379/0`，`LOG_SQL_DSN` 留空
5. `docker compose -f docker-compose.bundled.yml up -d` + 30 次健康检查（`GET /api/status` 含 `"success":true`，三容器 healthcheck 全绿才算过）

成功尾巴长这样：

```
[deploy] 部署成功: http://<本机IP>:3000 (源码 29e7401)
首次使用请初始化管理员: curl -X POST http://127.0.0.1:3000/api/setup ...
```

失败则看日志：

```bash
cd /app/new-api-prod && docker compose -f docker-compose.bundled.yml logs --tail=100
```

---

## 第 6 步：初始化管理员（部署成功后做一次）

```bash
# 把 3000 换成你的 APP_PORT，把 <强密码> 换成 8～128 位密码（用户名 ≤12 字符）
curl -X POST http://127.0.0.1:3000/api/setup \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<强密码>","confirmPassword":"<强密码>"}'
```

- 成功回 `{"success":true,...}`；再调一次报 `The system is already initialized` 属正常（防重复初始化）。
- 路由是 `POST /api/setup`。旧文档 `/api/user/setup` 会 404，已纠正。
- 然后浏览器打开 `http://<服务器公网IP>:<端口>`，admin 登录。

---

## 第 7 步：上线数据（二选一）

- **全新上线**：控制台手工建渠道 / 用户 / 令牌 / 分组。`系统设置 → ServerAddress` 改成 `http://<公网IP>:<端口>`。
- **从老库迁移**（sqlite → PG）：参考 `DEPLOY.md §配置迁移`，channels / options / abilities / users / tokens 五表全量导；
  注意 PG 布尔列 int→bool 转换 + 渠道密钥逐字节 sha 校验对过再切流量。

---

## 第 8 步：验证（部署当天必做，复制即跑）

```bash
PORT=3000   # 换成你的 APP_PORT

# 1) 健康（应打出 "success":true）
curl -s http://127.0.0.1:$PORT/api/status | grep -o '"success": *true'

# 2) 三容器全 healthy（三个都得是 healthy）
docker inspect --format '{{.Name}} {{.State.Health.Status}}' new-api-prod new-api-prod-postgres new-api-prod-redis

# 3) 密码真生效（xxx 换成你的 REDIS_PASSWORD_BUNDLED）
docker exec new-api-prod-postgres pg_isready -U postgres
# 应回 accepting connections
docker exec new-api-prod-redis redis-cli -a 'xxx' ping
# 应回 PONG

# 4) 真实中转（控制台建一把 key + auto 分组，先调通小模型；把 sk-xxx 换成你的 key）
curl -s http://127.0.0.1:$PORT/v1/chat/completions \
  -H "Authorization: Bearer sk-xxx" -H 'Content-Type: application/json' \
  -d '{"model":"glm-5.3-flash","messages":[{"role":"user","content":"ping"}]}' \
  -D - -o /dev/null | grep -i 'X-Using-Group'
# 应看到 X-Using-Group: <命中分组>（29e7401 二开透出，公司 Java 网关靠它做分账）
```

---

## 第 9 步：日常运维（bundled 版，背下来）

```bash
cd /app/new-api-prod
docker compose -f docker-compose.bundled.yml ps        # 看三容器状态
docker compose -f docker-compose.bundled.yml logs -f   # 跟日志（默认只跟 new-api，想看 pg 加服务名）
docker compose -f docker-compose.bundled.yml logs postgres --tail=100
docker compose -f docker-compose.bundled.yml restart   # 重启
docker compose -f docker-compose.bundled.yml down      # 停服（卷保留，数据不丢）
# ⚠️ 千万别 down -v，那会删 pg_data/redis_data 卷，数据全没

# 改密码/改配置后重建
docker compose -f docker-compose.bundled.yml up -d --force-recreate

# 从 fork 更新（先测试环境验，再 pin commit 上生产）
cd /root/newapi-src-prod && git pull
docker build -t new-api:prod-$(git rev-parse --short HEAD) .
# 回 /app/new-api-prod/.env 改 IMAGE_TAG，再 up -d --force-recreate

# 备份（停机或低峰做，复制即跑）
docker exec new-api-prod-postgres pg_dump -U postgres new-api | gzip > /root/newapi-backup-$(date +%F).sql.gz
docker run --rm -v new-api-prod_redis_data:/data -v /root:/bak alpine tar czf /bak/newapi-redis-$(date +%F).tgz -C /data .
ls -lh /root/newapi-backup-* /root/newapi-redis-*
```

敏感信息只有一份：`/app/new-api-prod/.env`（两容器密码、SESSION_SECRET），600 权限，勿提交进仓库。

---

## 附 A：密码轮换（已起服想换密码）

```bash
vim /app/new-api-prod/.env
# 改 POSTGRES_PASSWORD / REDIS_PASSWORD_BUNDLED 保存

cd /root/newapi-src-prod
./deploy/deploy.sh /app/new-api-prod/.env
# 再跑一次就行：脚本用新密码重组 DSN + compose up -d 重建
```

---

## 附 B：翻车速查（按报错直接对）

| 现象 | 原因 | 修法（复制执行） |
|---|---|---|
| `POST /api/user/setup` 404 | 路由错 | 用 `POST /api/setup`（第 6 步命令） |
| 健康检查 30×5s 超时 | build 慢 / 端口被占 / 安全组未放行 / pg 还没 ready | `docker compose -f docker-compose.bundled.yml logs --tail=100` 看首错；`ss -ltnp \| grep :端口` 查占用；云控制台查安全组；pg 首次启动慢就多等 1 分钟 |
| redis 报 `NOAUTH` / new-api 连不上 redis | `.env` 里 REDIS 密码与容器实际不一致（手改了没重建） | `up -d --force-recreate` 重建；仍不行核对 `grep REDIS /app/new-api-prod/.env` |
| PG 连不上 | POSTGRES_PASSWORD 含特殊字符 / 卷里是旧密码 | 密码先用纯字母数字；已起服换密码必须重跑 deploy.sh（DSN 跟着变） |
| `down -v` 后数据全没 | `-v` 删了命名卷 | 只能从附备份恢复；以后只用 `down` |
| 多节点 Session 互踢 | SESSION_SECRET 不一致 | 各节点 `.env` 填同一值后重建 |
| 容器名冲突 | `.env` 里 CONTAINER_NAME 与旧实例重名 | 改 CONTAINER_NAME 或先 `docker ps -a` 清掉旧容器 |

---

## 附 C：文件清单（都在仓库里，新机 clone 即得）

```
deploy/
├── deploy.sh                 # 一键脚本（git→build→密码→DSN→compose→健康检查）
├── docker-compose.yml         # host 模式用（本文不用）
├── docker-compose.bundled.yml # bundled：new-api + postgres:15 + redis:7 三容器（本文用这个）
└── .env.example               # 配置模板（GIT_REF 默认 29e7401，MW_MODE 默认 bundled）
DEPLOY.md                     # 老机部署现状 + 迁移记录（二开历史）
DEPLOY-NEW-SERVER.md          # 本文档（bundled 新机从零操作流程）
```
