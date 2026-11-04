# new-api 新服务器从零部署（单文档版，Ubuntu 24.04，fork @ baf8208）

> 目标读者：全新云服务器，什么都没装。SSH 以 root 连上后，照本文从头到尾复制执行即可。
> 部署包：`deploy/`（deploy.sh + host/bundled 双 compose + .env.example），默认 pin `29e7401`（group 筛选 + X-Using-Group + 一键包，已验证）。
> 全程 root 执行。预计耗时：环境约 10 分钟 + 构建约 3～8 分钟。

---

## 第 0 步：确认你在哪台机器、什么系统

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

## 第 1 步：换系统软件源并装基础工具（git/curl/常用）

```bash
# 更新软件索引 + 基础工具（-y 表示全自动 yes，不用逐个确认）
apt-get update && apt-get install -y git curl wget ca-certificates openssl python3 sudo

# 验证
git --version
curl --version | head -n 1
python3 --version
```

说明：`deploy.sh` 需要 `git` 拉源码、`curl` 做健康检查、`openssl` 生成随机密码、`python3` 组装 DSN、`sudo -u postgres` 建库（host 模式）。

---

## 第 2 步：装 Docker（含 compose 插件，新机必装）

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

## 第 3 步：选中间件模式（二选一，决定后面填什么）

| 模式 | 场景 | 你要做的 | 脚本替你做的 |
|---|---|---|---|
| `host`（默认，推荐） | 一台机只跑 new-api，想少两个容器 | 按第 4 步装好 PG/Redis（或让脚本自动 apt 装） | 建账号建库、放行容器网段、密码随机生成写回 `.env` |
| `bundled` | 纯空机，不想碰系统 PG/Redis | 什么都不用装，跳过第 4 步 | compose 顺手起 `postgres:15` + `redis:7-alpine` 两个容器，数据落 `pg_data` / `redis_data` 卷 |

host 模式强假设：apt 版 PG（`sudo -u postgres psql` 能进）+ systemd/service 其中之一。Docker 版 PG 不满足该假设，请改用 bundled。

---

## 第 4 步（仅 host 模式）：装 PostgreSQL + Redis（bundled 请跳过）

```bash
# 1) 安装（已有则跳过，deploy.sh 也会自动补装）
apt-get install -y postgresql postgresql-contrib redis-server

# 2) 开机自启 + 启动
systemctl enable --now postgresql
systemctl enable --now redis-server

# 3) 验证都在跑
systemctl is-active postgresql redis-server
psql --version
redis-cli ping
# redis 无密码时回 PONG；有密码回 (error) NOAUTH Authentication required，属正常，后面填进 .env 即可

# 4) 查询本机 Redis 是否有密码、bind 现状（deploy.sh 会自动改，这里先看一眼）
redis-cli --raw CONFIG GET requirepass | tail -1   # 空=无密码
redis-cli --raw CONFIG GET bind | tail -1          # 看 bind 行
```

---

## 第 5 步：拉仓库 + 写配置（全文只改这一份 `.env`）

```bash
# 1) 拉 fork 源码（仓库里自带 deploy/）
git clone https://github.com/necor0703/new-api.git /root/newapi-src-prod
cd /root/newapi-src-prod
git rev-parse --short HEAD   # 记下短 SHA，应为 29e7401（或 baf8208 之后的新 commit）

# 2) 准备配置
mkdir -p /app/new-api-prod
cp deploy/.env.example /app/new-api-prod/.env
chmod 600 /app/new-api-prod/.env
vim /app/new-api-prod/.env
```

`.env` 逐项怎么填（`vim` 里按 `i` 编辑，`Esc` 后 `:wq` 保存）：

```ini
# --- 必看三项 ---
GIT_REF=29e7401          # 别改 main！生产 pin 已验证 commit
APP_PORT=3000            # 对外端口；改了它，第 0 步安全组也要改
MW_MODE=host             # host=复用本机PG/Redis；bundled=compose起pg+redis容器

# --- host 模式才看 ---
PG_DB=newapi             # 默认即可，脚本自动建
PG_LOG_DB=newapi_log     # 日志库，脚本自动建
PG_USER=newapi           # 默认即可
PG_PASSWORD=             # 留空=脚本随机生成写回；想自定就填一个强密码
REDIS_PASSWORD=          # 本机 Redis 密码：无密码留空，有密码必填（否则脚本连不上 redis 改 bind）
REDIS_DB=1               # 换个空库号，避免和别的业务串库（先 redis-cli -n 1 DBSIZE 确认是 0）

# --- bundled 模式才看（host 请忽略） ---
POSTGRES_PASSWORD=       # 留空=随机生成
REDIS_PASSWORD_BUNDLED=  # 留空=随机生成

# --- 以下三行不要手填，脚本组装 ---
# SQL_DSN=
# LOG_SQL_DSN=
# REDIS_CONN_STRING=
# SESSION_SECRET=        # 留空=随机生成；多节点必须各节点填一样
```

填完自检：

```bash
grep -E '^(APP_PORT|MW_MODE|GIT_REF|REDIS_PASSWORD|REDIS_DB)=' /app/new-api-prod/.env
ss -ltnp | grep ":$(grep '^APP_PORT=' /app/new-api-prod/.env | cut -d= -f2)" || echo "端口空闲，OK"
```

---

## 第 6 步：一键部署（一条命令，约 3～8 分钟）

```bash
cd /root/newapi-src-prod
./deploy/deploy.sh /app/new-api-prod/.env
```

脚本内部做的事（不用你动手，出错对照看）：
1. `git clone/pull` 到 `GIT_REF`（pin 住就是 29e7401）
2. `docker build -t IMAGE_TAG`（三阶段：bun 前端 → golang:1.26 后端 → debian-slim，约 340MB）
3. 落 compose 文件到 `/app/new-api-prod/`，`chmod 600 .env`
4. host：建 PG 账号/双库、pg_hba 放行 `172.16.0.0/12` + `10.0.0.0/8`（compose 自建网段源 IP 不固定，只放 docker0 /32 会 28000）、redis bind 加网关 IP、组装三行 DSN 写回
   bundled：随机密码、组装容器内 DSN（`postgres`/`redis` 服务名，LOG_SQL_DSN 留空=复用主库，代码 `model/main.go InitLogDB` 支持）
5. `docker compose up -d` + 30 次健康检查（`GET /api/status` 含 `"success":true`）

成功尾巴长这样：

```
[deploy] 部署成功: http://<本机IP>:3000 (源码 29e7401)
首次使用请初始化管理员: curl -X POST http://127.0.0.1:3000/api/setup ...
```

失败则看日志（密码里不打码的是 `.env`，日志里看不到明文密码）：

```bash
cd /app/new-api-prod && docker compose logs --tail=100
```

---

## 第 7 步：初始化管理员（部署成功后做一次）

```bash
# 把 3000 换成你的 APP_PORT，把 <强密码> 换成 8～128 位密码（用户名 ≤12 字符）
curl -X POST http://127.0.0.1:3000/api/setup \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<强密码>","confirmPassword":"<强密码>"}'
```

- 成功回 `{"success":true,...}`；再调一次报 `The system is already initialized` 属正常（防重复初始化）。
- 路由是 `POST /api/setup`（`router/api-router.go:27`）。旧文档 `/api/user/setup` 会 404，已纠正。
- 然后浏览器打开 `http://<服务器公网IP>:<端口>`，admin 登录。

---

## 第 8 步：上线数据（二选一）

- **全新上线**：控制台手工建渠道 / 用户 / 令牌 / 分组。`系统设置 → ServerAddress` 改成 `http://<公网IP>:<端口>`。
- **从老库迁移**（sqlite → PG）：参考 `DEPLOY.md §配置迁移`，channels / options / abilities / users / tokens 五表全量导；
  注意 PG 布尔列 int→bool 转换 + 渠道密钥逐字节 sha 校验对过再切流量。

---

## 第 9 步：验证（部署当天必做，复制即跑）

```bash
PORT=3000   # 换成你的 APP_PORT

# 1) 健康（应打出 "success":true）
curl -s http://127.0.0.1:$PORT/api/status | grep -o '"success": *true'

# 2) 容器健康（应为 healthy）
docker inspect --format '{{.State.Health.Status}}' new-api-prod

# 3) 真实中转（控制台建一把 key + auto 分组，先调通小模型；把 sk-xxx 换成你的 key）
curl -s http://127.0.0.1:$PORT/v1/chat/completions \
  -H "Authorization: Bearer sk-xxx" -H 'Content-Type: application/json' \
  -d '{"model":"glm-5.3-flash","messages":[{"role":"user","content":"ping"}]}' \
  -D - -o /dev/null | grep -i 'X-Using-Group'
# 应看到 X-Using-Group: <命中分组>（29e7401 二开透出，公司 Java 网关靠它做分账）
```

---

## 第 10 步：日常运维（背下来）

```bash
cd /app/new-api-prod
docker compose ps               # 看状态
docker compose logs -f          # 跟日志
docker compose logs --tail=200  # 看最近 200 行
docker compose restart          # 重启
docker compose down             # 停服（数据在 PG/Redis 里，不丢）
docker compose up -d --force-recreate   # 改配置后重建

# 从 fork 更新（先测试环境验，再 pin commit 上生产）
cd /root/newapi-src-prod && git pull
docker build -t new-api:prod-$(git rev-parse --short HEAD) .
# 回 /app/new-api-prod/.env 改 IMAGE_TAG，再 up -d --force-recreate
```

敏感信息只有一份：`/app/new-api-prod/.env`（PG/Redis 密码、SESSION_SECRET），600 权限，勿提交进仓库（`.gitignore` 已忽略 `.env`）。

---

## 附：翻车速查（按报错直接对）

| 现象 | 原因 | 修法（复制执行） |
|---|---|---|
| `POST /api/user/setup` 404 | 路由错 | 用 `POST /api/setup`（第 7 步命令） |
| PG `28000` / `password authentication failed` | pg_hba 没放行容器网段 | 脚本已放行 `172.16.0.0/12` + `10.0.0.0/8`；手工补：`sudo -u postgres psql -c "SELECT pg_reload_conf();"` 后重试；仍不行看 `docker compose logs` 首错 |
| 改了 postgresql.conf listen 没生效 | listen 改动要 restart，reload 不够 | `systemctl restart postgresql` |
| Redis 改完 bind 起不来 | 改错了 conf / 语法错 | 对 `ps -o args= -C redis-server` 找到的真实 conf 改：`bind 127.0.0.1 ::1 <网关IP>`，再 `systemctl restart redis-server` |
| 健康检查 30×5s 超时 | build 慢 / 端口被占 / 安全组未放行 | `docker compose logs --tail=100` 看首错；`ss -ltnp \| grep :端口` 查占用；云控制台查安全组 |
| `could not read Username for github` | 新机无凭证（只影响 push，不影响部署） | 部署用匿名 clone 即可；要推代码才需 token/ssh |
| 多节点 Session 互踢 | SESSION_SECRET 不一致 | 各节点 `.env` 填同一值后重建 |
| `docker compose` 提示 command not found | 只装了 docker 没装插件 | 回第 2 步重装 `docker-compose-plugin` |

---

## 附：文件清单（都在仓库里，新机 clone 即得）

```
deploy/
├── deploy.sh                 # 一键脚本（git→build→中间件→compose→健康检查）
├── docker-compose.yml         # host 模式：只起 new-api，连本机 PG/Redis
├── docker-compose.bundled.yml # bundled 模式：new-api + postgres:15 + redis:7 三容器
└── .env.example               # 配置模板（GIT_REF 默认 29e7401）
DEPLOY.md                     # 老机部署现状 + 迁移记录（二开历史）
DEPLOY-NEW-SERVER.md          # 本文档（新机从零操作流程）
```
