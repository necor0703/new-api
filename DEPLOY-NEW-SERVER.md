# new-api 新服务器部署（bundled 版）

> 适用：Amazon Linux 2023，ec2-user 执行（非 root，需要 sudo 提权）。
> 3 个容器：`new-api` + `postgres:15` + `redis:7`。全程 5 步，约 15 分钟（构建占大头）。

## 0. 安全组

EC2 → 安全组 → Inbound 加一条 **TCP 3000 / 0.0.0.0/0**（跟下面 `APP_PORT` 一致）。

## 1. 装 Docker

```bash
whoami  # 确认是 ec2-user
sudo dnf update -y
sudo dnf install -y git curl ca-certificates openssl python3 nano
hash -r
git --version  # 有版本号才算成；若仍 command not found，先跑 hash -r 再试
curl -fsSL https://get.docker.com -o get-docker.sh
sudo sh get-docker.sh
sudo systemctl enable --now docker
sudo systemctl is-active docker
sudo docker --version
sudo docker compose version
```

免 sudo（跑完重登一次 SSH 生效，之后 docker 命令可不加 sudo）：

```bash
sudo usermod -aG docker ec2-user
newgrp docker
docker ps
```

> `get.docker.com` 是官方脚本，`docker-ce + compose-plugin` 一次装齐。
> `systemctl enable --now docker` 是开机自启开关，别漏。
> 下面步骤里 `sudo docker` 在免 sudo 生效后可去掉 sudo。

## 2. 拉代码，改配置（改 4 项）

```bash
git clone https://github.com/necor0703/new-api.git ~/newapi-src-prod
sudo mkdir -p /app/new-api-prod
sudo chown -R ec2-user:ec2-user /app/new-api-prod
cp ~/newapi-src-prod/deploy/.env.example /app/new-api-prod/.env
chmod 600 /app/new-api-prod/.env
nano /app/new-api-prod/.env
```

> 顺序别反：先 `chown` 再改 `.env`，否则 `sed/nano` 报 `Permission denied`。
> 若已报 `Permission denied`，补跑 `sudo chown -R ec2-user:ec2-user /app/new-api-prod` 即可。

`.env` 里改这 4 行，其余别动（第 4 行是 ec2-user 必改项，root 用户不用改）：

```ini
APP_PORT=3000
POSTGRES_PASSWORD=Pg2026_NewApi_01
REDIS_PASSWORD_BUNDLED=Rs2026_NewApi_02
SRC_DIR=/home/ec2-user/newapi-src-prod
```

> 密码用 16 位以上字母+数字，示例别照抄。留空也行，脚本会自动随机生成写回。
> `SRC_DIR` 不改会报 `mkdir: cannot create directory '/root': Permission denied`，因为模板默认写的是 root 家目录。

## 3. 一键部署

```bash
cd ~/newapi-src-prod && ./deploy/deploy.sh /app/new-api-prod/.env
```

成功会打印 `部署成功: http://<IP>:3000`。失败就看日志：

```bash
cd /app/new-api-prod && sudo docker compose -f docker-compose.bundled.yml logs --tail=100
```

> 已部署但 redis 报 `unhealthy`（老包 bug）：补上 healthcheck 的环境变量后重建即可：
>
> ```bash
> sed -i '/--requirepass/a\    environment:\n      - REDIS_PASSWORD_BUNDLED=${REDIS_PASSWORD_BUNDLED}' /app/new-api-prod/docker-compose.bundled.yml
> grep -A2 'requirepass' /app/new-api-prod/docker-compose.bundled.yml  # 确认已插入
> cd /app/new-api-prod && sudo docker compose -f docker-compose.bundled.yml up -d --force-recreate
> ```
>
> 新 clone 的代码已自带该修复，不用再手动补。

## 4. 建管理员（只做一次）

```bash
curl -X POST http://127.0.0.1:3000/api/setup \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<8位以上密码>","confirmPassword":"<8位以上密码>"}'
```

然后浏览器打开 `http://<服务器IP>:3000`，admin 登录建渠道/用户/key。查外网 IP：

```bash
curl -s http://169.254.169.254/latest/meta-data/public-ipv4  # AWS 元数据，最准
curl -s https://api.ipify.org; echo                            # 无公网 IP 时看出口 IP
```

## 5. 验证（3 条）

```bash
curl -s http://127.0.0.1:3000/api/status | grep -o '"success": *true'
sudo docker inspect --format '{{.Name}} {{.State.Health.Status}}' new-api-prod new-api-prod-postgres new-api-prod-redis
sudo systemctl is-enabled docker
# 前两条有输出、三个 healthy、最后一条 enabled 才算过
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

## 附：版本更新（代码发布）

代码有更新（fork main 已有新提交）时，一条命令完成拉代码→重新打包→发布：

```bash
cd ~/newapi-src-prod && ./deploy/deploy.sh /app/new-api-prod/.env
```

脚本幂等，重复执行安全：

- 自动 `git pull` main → `docker build new-api:prod-latest` → `compose up -d`（镜像变了才重建容器）
- `.env` 已有密码/密钥不重新生成，PG 数据卷不动，用户/渠道/日志全保留
- 成功标志：`部署成功: http://<IP>:3000 (源码 <commit短哈希>)`，哈希可对上刚推的 commit
- 验证：`curl -s http://127.0.0.1:3000/api/status | grep -o '"success": *true'`，顺手 `docker image prune -f` 清旧镜像层

## 附：翻车 5 条

| 现象 | 修法 |
|---|---|
| `/api/user/setup` 404 | 路由是 `/api/setup`，用第 4 步命令 |
| 健康检查超时 | `logs --tail=100` 看首错；查端口占用 `ss -ltnp \| grep :3000`；查安全组 |
| redis `NOAUTH` | 改完密码没重建：`up -d --force-recreate` |
| `down -v` 后数据没了 | `-v` 会删卷，只能从备份恢复；以后只用 `down` |
| 换密码 | 改 `.env` 两个密码，重跑一遍 `deploy.sh` 即可 |
| `dnf` 报 superuser privileges | 你是 ec2-user，`dnf/systemctl` 前面加 `sudo` |
| `docker: permission denied` | 没进 docker 组：跑第 1 步的 `usermod + newgrp`，或重登 SSH |
| `git: command not found`（但 dnf 说已安装） | bash 缓存旧 PATH：跑 `hash -r` 再试 |
| `sed: couldn't open temporary file` / `.env Permission denied` | `/app/new-api-prod` 归属不对：`sudo chown -R ec2-user:ec2-user /app/new-api-prod` |
| redis `unhealthy`，new-api 起不来 | 老包 bug：compose 里补 redis `environment` 后 `up -d --force-recreate`（见第 3 步） |
| `mkdir: cannot create directory '/root'` | `.env` 里 `SRC_DIR` 还是 root 家目录：改成 `/home/ec2-user/newapi-src-prod` |
