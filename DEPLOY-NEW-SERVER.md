# new-api 新服务器部署（bundled 版）

> Ubuntu 22.04/24.04 或 Amazon Linux 2023，3 个容器：`new-api` + `postgres:15` + `redis:7`。
> 全程 5 步，约 15 分钟（构建占大头）。

## 0. 安全组

云控制台放行 **TCP 3000**（跟下面 `APP_PORT` 一致）。
AWS EC2：在实例的安全组 Inbound 加一条 TCP 3000 / 0.0.0.0/0。

## 1. 装 Docker（二选一）

### A. Ubuntu（root 执行）

```bash
apt-get update && apt-get install -y git curl ca-certificates openssl python3 sudo
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" > /etc/apt/sources.list.d/docker.list
apt-get update && apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
systemctl enable --now docker
docker compose version
```

### B. Amazon Linux 2023（ec2-user 执行，全程加 sudo）

```bash
sudo dnf update -y
sudo dnf install -y git curl ca-certificates openssl python3 nano
sudo dnf install -y docker
sudo systemctl enable --now docker
sudo systemctl is-active docker
```

补 compose 插件（AL2023 自带 docker 一般没有）：

```bash
sudo docker compose version || echo "NEED_COMPOSE_PLUGIN"
# 若提示 'compose' is not a docker command，才跑下面 4 行：
ARCH=$(uname -m); [ "$ARCH" = "x86_64" ] && ARCH=x86_64 || ARCH=aarch64
sudo mkdir -p /usr/local/lib/docker/cli-plugins
sudo curl -SL "https://github.com/docker/compose/releases/latest/download/docker-compose-linux-${ARCH}" -o /usr/local/lib/docker/cli-plugins/docker-compose
sudo chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
sudo docker compose version
```

免 sudo（跑完重登一次 SSH 生效）：

```bash
sudo usermod -aG docker ec2-user
newgrp docker
```

> 后面步骤里把 `docker` 换成 `sudo docker` 即可（免 sudo 生效后可不加）。
> 不确定系统？跑 `cat /etc/os-release` 看 `NAME=`，有 `dnf` 走 B，有 `apt-get` 走 A。

## 2. 拉代码，改配置（只改 3 项）

```bash
# root：git clone https://github.com/necor0703/new-api.git /root/newapi-src-prod
# ec2-user：
git clone https://github.com/necor0703/new-api.git ~/newapi-src-prod
sudo mkdir -p /app/new-api-prod
sudo chown $(whoami):$(whoami) /app/new-api-prod
cp ~/newapi-src-prod/deploy/.env.example /app/new-api-prod/.env
# root 用户把上面 ~ 换成 /root 即可
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
cd ~/newapi-src-prod && ./deploy/deploy.sh /app/new-api-prod/.env
# root 用户：cd /root/newapi-src-prod && ./deploy/deploy.sh /app/new-api-prod/.env
```

成功会打印 `部署成功: http://<IP>:3000`。失败就看日志：

```bash
cd /app/new-api-prod && sudo docker compose -f docker-compose.bundled.yml logs --tail=100
# root 用户不用 sudo
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
sudo docker inspect --format '{{.Name}} {{.State.Health.Status}}' new-api-prod new-api-prod-postgres new-api-prod-redis
# 三个都是 healthy 才算过（root 用户去掉 sudo）
```

---

## 附：运维 4 条

```bash
cd /app/new-api-prod
sudo docker compose -f docker-compose.bundled.yml ps          # 状态
sudo docker compose -f docker-compose.bundled.yml logs -f     # 日志
sudo docker compose -f docker-compose.bundled.yml restart    # 重启
sudo docker compose -f docker-compose.bundled.yml down       # 停服（数据保留，千万别加 -v）
```

## 附：翻车 5 条

| 现象 | 修法 |
|---|---|
| `/api/user/setup` 404 | 路由是 `/api/setup`，用第 4 步命令 |
| 健康检查超时 | `logs --tail=100` 看首错；查端口占用 `ss -ltnp \| grep :3000`；查安全组 |
| redis `NOAUTH` | 改完密码没重建：`up -d --force-recreate` |
| `down -v` 后数据没了 | `-v` 会删卷，只能从备份恢复；以后只用 `down` |
| 换密码 | 改 `.env` 两个密码，重跑一遍 `deploy.sh` 即可 |
