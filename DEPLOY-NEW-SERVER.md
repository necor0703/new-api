# new-api 新服务器部署（bundled 版）

> 适用：Amazon Linux 2023，root 执行。3 个容器：`new-api` + `postgres:15` + `redis:7`。
> 全程 5 步，约 15 分钟（构建占大头）。

## 0. 安全组

EC2 → 安全组 → Inbound 加一条 **TCP 3000 / 0.0.0.0/0**（跟下面 `APP_PORT` 一致）。

## 1. 装 Docker

```bash
whoami  # 确认是 root
dnf update -y
dnf install -y git curl ca-certificates openssl python3 nano
curl -fsSL https://get.docker.com -o get-docker.sh
sh get-docker.sh
systemctl enable --now docker
systemctl is-active docker
docker --version
docker compose version
```

> `get.docker.com` 是官方脚本，`docker-ce + compose-plugin` 一次装齐，后续 `dnf update` 跟着升级。
> `systemctl enable --now docker` 是开机自启开关，别漏。

## 2. 拉代码，改配置（只改 3 项）

```bash
git clone https://github.com/necor0703/new-api.git /root/newapi-src-prod
mkdir -p /app/new-api-prod
cp /root/newapi-src-prod/deploy/.env.example /app/new-api-prod/.env
chmod 600 /app/new-api-prod/.env
nano /app/new-api-prod/.env
```

`.env` 里只改这 3 行，其余别动：

```ini
APP_PORT=3000
POSTGRES_PASSWORD=Pg2026_NewApi_01
REDIS_PASSWORD_BUNDLED=Rs2026_NewApi_02
```

> 密码用 16 位以上字母+数字，示例别照抄。留空也行，脚本会自动随机生成写回。

## 3. 一键部署

```bash
cd /root/newapi-src-prod && ./deploy/deploy.sh /app/new-api-prod/.env
```

成功会打印 `部署成功: http://<IP>:3000`。失败就看日志：

```bash
cd /app/new-api-prod && docker compose -f docker-compose.bundled.yml logs --tail=100
```

## 4. 建管理员（只做一次）

```bash
curl -X POST http://127.0.0.1:3000/api/setup \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<8位以上密码>","confirmPassword":"<8位以上密码>"}'
```

然后浏览器打开 `http://<服务器IP>:3000`，admin 登录建渠道/用户/key。

## 5. 验证（3 条）

```bash
curl -s http://127.0.0.1:3000/api/status | grep -o '"success": *true'
docker inspect --format '{{.Name}} {{.State.Health.Status}}' new-api-prod new-api-prod-postgres new-api-prod-redis
systemctl is-enabled docker
# 前两条有输出、三个 healthy、最后一条 enabled 才算过
```

---

## 附：运维 4 条

```bash
cd /app/new-api-prod
docker compose -f docker-compose.bundled.yml ps          # 状态
docker compose -f docker-compose.bundled.yml logs -f     # 日志
docker compose -f docker-compose.bundled.yml restart    # 重启
docker compose -f docker-compose.bundled.yml down       # 停服（数据保留，千万别加 -v）
```

## 附：翻车 5 条

| 现象 | 修法 |
|---|---|
| `/api/user/setup` 404 | 路由是 `/api/setup`，用第 4 步命令 |
| 健康检查超时 | `logs --tail=100` 看首错；查端口占用 `ss -ltnp \| grep :3000`；查安全组 |
| redis `NOAUTH` | 改完密码没重建：`up -d --force-recreate` |
| `down -v` 后数据没了 | `-v` 会删卷，只能从备份恢复；以后只用 `down` |
| 换密码 | 改 `.env` 两个密码，重跑一遍 `deploy.sh` 即可 |
