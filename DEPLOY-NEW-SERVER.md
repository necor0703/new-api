# 新服务器 new-api 一键部署操作流程（necor0703 fork @ 29e7401）

> 适用：全新 Ubuntu/Debian 服务器（amd64 / arm64），root 执行。
> 部署包：`deploy/`（deploy.sh + docker-compose.yml + docker-compose.bundled.yml + .env.example）
> 默认版本：`GIT_REF=29e7401`（group 筛选 + X-Using-Group + 一键包，已在本机 3002 验证）。

---

## 0. 前置（新机一次性）

```bash
# 1) 工具：docker + git + curl（deploy.sh 会自检，缺了先装）
docker --version && docker compose version && git --version && curl --version

# 2) 端口：APP_PORT 默认 3000，未被占用 + 云安全组放行
ss -ltnp | grep ':3000' || echo "3000 空闲"
# 云控制台放行 TCP 3000（或你改的 APP_PORT）

# 3) 以 root 登录（脚本要 sudo -u postgres 建库 + 改 PG/Redis 配置）
whoami   # 必须是 root
```

---

## 1. 选中间件模式（二选一，.env 里只填 MW_MODE）

| 模式 | 场景 | 脚本动作 |
|---|---|---|
| `host`（默认） | 本机已有 PG/Redis，想复用 | 没有就 apt 装；自动建账号建库、放行容器网段、密码随机生成写回 `.env` |
| `bundled` | 全新空机，啥也没有 | compose 顺手起 `postgres:15` + `redis:7-alpine` 两个容器，数据落 `pg_data` / `redis_data` 卷 |

host 模式强假设：apt 版 PG（`sudo -u postgres psql` 能进）+ systemd/service 其中之一。
Docker 版 PG 不满足该假设，请改用 bundled。

---

## 2. 部署（三条命令）

```bash
# 1) 拉仓库（仓库里自带 deploy/）
git clone https://github.com/necor0703/new-api.git /root/newapi-src-prod
cd /root/newapi-src-prod

# 2) 准备配置（只改这一份）
cp deploy/.env.example /app/new-api-prod/.env
vim /app/new-api-prod/.env
# 必改：APP_PORT（对外端口）/ MW_MODE（host 或 bundled）/ 密码类
# host 模式：REDIS_PASSWORD 填本机 Redis 密码（无密码留空），REDIS_DB 换个空库号避免串库
# bundled 模式：POSTGRES_PASSWORD / REDIS_PASSWORD_BUNDLED 留空=脚本随机生成
# 其余 GIT_REF / IMAGE_TAG / 库名默认即可；SQL_DSN 等三行不要手填，脚本组装

# 3) 一键部署（git拉码 → docker构建约3-8分钟 → 中间件 → compose起服 → 健康检查）
./deploy/deploy.sh /app/new-api-prod/.env
```

脚本做完打印访问地址：
`部署成功: http://<本机IP>:<端口> (源码 29e7401)`

---

## 3. 初始化管理员（部署成功后一次）

```bash
curl -X POST http://127.0.0.1:<端口>/api/setup \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<强密码>","confirmPassword":"<强密码>"}'
```

然后浏览器打开 `http://<服务器IP>:<端口>`，admin 登录。

> 注意：路由是 `POST /api/setup`（旧文档里 `/api/user/setup` 是错的，已纠正）。
> 用户名 ≤12 字符；设过一次后再次调用报"The system is already initialized"，属正常。

---

## 4. 数据（二选一）

- **全新上线**：控制台手工建渠道 / 用户 / 令牌 / 分组即可。`ServerAddress` 按新机 IP:端口改。
- **从老库迁移**（sqlite → PG）：参考 `DEPLOY.md §配置迁移`，channels / options / abilities / users / tokens 五表全量导，
  注意 PG 布尔列 int→bool 转换 + 渠道密钥逐字节 sha 校验。

---

## 5. 验证（部署当天必做）

```bash
# 1) 健康
curl -s http://127.0.0.1:<端口>/api/status | grep -o '"success": *true'

# 2) 真实中转（用新建的 key + auto 分组调一个小模型）
curl -s http://127.0.0.1:<端口>/v1/chat/completions \
  -H "Authorization: Bearer sk-xxx" -H 'Content-Type: application/json' \
  -d '{"model":"glm-5.3-flash","messages":[{"role":"user","content":"ping"}]}' \
  -D - -o /dev/null | grep -i 'X-Using-Group'
# 应看到 X-Using-Group: <命中分组>（29e7401 二开透出，公司 Java 网关靠它做分账）
```

---

## 6. 日常运维

```bash
cd /app/new-api-prod
docker compose ps            # 看状态
docker compose logs -f       # 看日志
docker compose restart       # 重启
docker compose down          # 停服（数据在 PG/Redis 里，不丢）

# 从 fork 更新（先在测试环境验，再 pin commit 上生产）
cd /root/newapi-src-prod && git pull
docker build -t new-api:prod-<短SHA> .
# 改 .env 里 IMAGE_TAG 后
cd /app/new-api-prod && docker compose up -d --force-recreate
```

敏感信息位置：`/app/new-api-prod/.env`（PG/Redis 密码、SESSION_SECRET），600 权限，勿提交进仓库。

---

## 7. 翻车速查

| 现象 | 原因 | 修法 |
|---|---|---|
| `POST /api/user/setup` 404 | 路由错 | 用 `POST /api/setup` |
| PG `28000` 连不上 | pg_hba 只放了 docker0 /32，compose 自建网段源 IP 不固定 | 脚本已放行 `172.16.0.0/12` + `10.0.0.0/8`；手工改需 reload |
| 改了 postgresql.conf listen 没生效 | listen 改动要 restart，reload 不够 | `systemctl restart postgresql` |
| Redis 改完 bind 起不来 | redis.conf 路径找错 / bind 语法错 | 对 `ps` 找到的真实 conf 改，`bind 127.0.0.1 ::1 <网关IP>` 无尾斜杠 |
| 健康检查 30×5s 超时 | build 慢 / 端口被占 / 云安全组未放行 | `docker compose logs --tail=100` 看首错；`ss -ltnp` 查端口 |
| 多节点 Session 互踢 | SESSION_SECRET 不一致 | 各节点 `.env` 用同一值 |
