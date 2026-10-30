#!/usr/bin/env bash
# new-api 生产一键部署脚本：git 拉源码 -> docker build -> 装/复用 PG+Redis -> compose 起服
# 用法：
#   cp .env.example /app/new-api-prod/.env   # 按需改 GIT_REPO/GIT_REF/APP_PORT/MW_MODE/密码
#   ./deploy.sh [/app/new-api-prod/.env]
set -euo pipefail

ENV_FILE="${1:-./.env}"
[[ -f "$ENV_FILE" ]] || { echo "找不到 $ENV_FILE，先从 .env.example 复制一份再改"; exit 1; }
# shellcheck disable=SC1090
set -a; source "$ENV_FILE"; set +a

# 默认值（.env 缺项也不炸，set -u 安全）
: "${SRC_DIR:=/root/newapi-src-prod}"
: "${DEPLOY_DIR:=/app/new-api-prod}"
: "${GIT_REPO:=https://github.com/necor0703/new-api.git}"
: "${GIT_REF:=main}"
: "${IMAGE_TAG:=new-api:prod-latest}"
: "${CONTAINER_NAME:=new-api-prod}"
: "${APP_PORT:=3000}"
: "${MW_MODE:=host}"
: "${PG_DB:=newapi}"
: "${PG_LOG_DB:=newapi_log}"
: "${PG_USER:=newapi}"
: "${PG_PORT:=5432}"
: "${REDIS_PORT:=6379}"
: "${REDIS_DB:=1}"
: "${NODE_NAME:=new-api-prod-node-1}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mkdir -p "$DEPLOY_DIR" "$SRC_DIR"

log() { echo "[deploy] $*"; }
rand_hex() { openssl rand -hex "${1:-32}"; }
# URL 编码（密码含 @/:?&|= 等特殊字符时 DSN 不炸）
urlencode() { python3 -c 'import sys,urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$1"; }

# 写回 .env（存在则替换，不存在则追加；sed 分隔符用 |，值含 | & \ 时转义）
env_set() {
  local k="$1" v="$2" f="$ENV_FILE"
  local esc
  esc="$(printf '%s' "$v" | sed -e 's/[\\&|]/\\&/g')"
  if grep -qE "^${k}=" "$f"; then
    sed -i "s|^${k}=.*|${k}=${esc}|" "$f"
  else
    printf '%s=%s\n' "$k" "$v" >> "$f"
  fi
}

# 用 python 精确替换 .env 中三行 DSN（值含 :/@ 等特殊字符也不炸）
env_set_dsns() {
  SQL_DSN="$1" LOG_SQL_DSN="$2" REDIS_CONN_STRING="$3" ENV_FILE="$ENV_FILE" python3 - <<'PYEOF'
import os
f = os.environ['ENV_FILE']
vals = {'SQL_DSN': os.environ['SQL_DSN'], 'LOG_SQL_DSN': os.environ['LOG_SQL_DSN'], 'REDIS_CONN_STRING': os.environ['REDIS_CONN_STRING']}
lines = open(f).read().splitlines()
out, seen = [], set()
for ln in lines:
    k = ln.split('=', 1)[0]
    if k in vals:
        out.append(k + '=' + vals[k]); seen.add(k)
    else:
        out.append(ln)
for k, v in vals.items():
    if k not in seen:
        out.append(k + '=' + v)
open(f, 'w').write('\n'.join(out) + '\n')
PYEOF
}

command -v docker >/dev/null || { echo "没装 docker，先装 docker"; exit 1; }
command -v git >/dev/null || { echo "没装 git，先装 git"; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "docker compose 插件不可用"; exit 1; }

# ---------- 1. 拉源码（分支 pull，tag/SHA 则 detached 不 pull） ----------
if [[ ! -d "$SRC_DIR/.git" ]]; then
  log "clone $GIT_REPO -> $SRC_DIR"
  git clone "$GIT_REPO" "$SRC_DIR"
fi
cd "$SRC_DIR"
git fetch origin >/dev/null 2>&1 || true
if git show-ref --verify --quiet "refs/heads/$GIT_REF"; then
  git checkout "$GIT_REF" >/dev/null 2>&1
  git pull --ff-only >/dev/null 2>&1 || log "git pull 跳过（无上游/已最新）"
elif git show-ref --verify --quiet "refs/tags/$GIT_REF"; then
  git checkout "$GIT_REF" >/dev/null 2>&1
else
  # commit SHA 或远端分支
  git checkout "$GIT_REF" >/dev/null 2>&1 || git checkout -B "$GIT_REF" "origin/$GIT_REF"
fi
SHORT_SHA="$(git rev-parse --short HEAD)"
log "源码就绪: $(git log -1 --format='%h %s')"

# ---------- 2. 构建镜像 ----------
log "docker build $IMAGE_TAG（约 3-8 分钟）"
docker build -t "$IMAGE_TAG" "$SRC_DIR"

# ---------- 3. 落 compose 文件 ----------
[[ -f "$DEPLOY_DIR/docker-compose.yml" ]] || cp "$SCRIPT_DIR/docker-compose.yml" "$DEPLOY_DIR/docker-compose.yml"
[[ -f "$DEPLOY_DIR/docker-compose.bundled.yml" ]] || cp "$SCRIPT_DIR/docker-compose.bundled.yml" "$DEPLOY_DIR/docker-compose.bundled.yml"
chmod 600 "$ENV_FILE"

# ---------- 4. 密钥 ----------
if [[ -z "${SESSION_SECRET:-}" ]]; then
  SESSION_SECRET="$(rand_hex 32)"; export SESSION_SECRET
  env_set SESSION_SECRET "$SESSION_SECRET"
  log "SESSION_SECRET 已随机生成并写回 .env"
fi

# ---------- 5. 中间件 ----------
DOCKER_GW="$(ip -4 addr show docker0 2>/dev/null | grep -oP '(?<=inet\s)\d+\.\d+\.\d+\.\d+' | head -1 || true)"
DOCKER_GW="${DOCKER_GW:-172.17.0.1}"

if [[ "$MW_MODE" == "host" ]]; then
  # --- 5a. 没有就装 ---
  if ! command -v psql >/dev/null; then
    log "未检测到 PostgreSQL，apt 安装…"
    apt-get update && apt-get install -y postgresql postgresql-contrib
  fi
  if ! command -v redis-server >/dev/null; then
    log "未检测到 Redis，apt 安装…"
    apt-get update && apt-get install -y redis-server
  fi
  if command -v systemctl >/dev/null 2>&1; then
    systemctl enable --now postgresql >/dev/null 2>&1 || service postgresql start || true
    systemctl enable --now redis-server >/dev/null 2>&1 || service redis-server start || true
  else
    service postgresql start || true
    service redis-server start || true
  fi

  # --- 5b. PG：建账号建库（密码单引号转义防 SQL 断裂） ---
  [[ -z "${PG_PASSWORD:-}" ]] && { PG_PASSWORD="$(rand_hex 16)"; export PG_PASSWORD; env_set PG_PASSWORD "$PG_PASSWORD"; log "PG 密码已随机生成写回 .env"; }
  PG_PASSWORD_SQL="${PG_PASSWORD//\'/\'\'}"
  sudo -u postgres psql -v ON_ERROR_STOP=1 <<EOF
DO \$\$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='${PG_USER}') THEN CREATE ROLE "${PG_USER}" LOGIN PASSWORD '${PG_PASSWORD_SQL}'; END IF; END \$\$;
EOF
  for db in "$PG_DB" "$PG_LOG_DB"; do
    if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='$db'" | grep -q 1; then
      sudo -u postgres createdb -O "$PG_USER" "$db"
      log "建库 $db (owner=$PG_USER)"
    fi
  done
  sudo -u postgres psql -c "ALTER ROLE \"$PG_USER\" WITH PASSWORD '$PG_PASSWORD_SQL';"

  # --- 5c. 放行容器网段连本机 PG/Redis ---
  # 注意：compose 默认自建 172.20.0.0/16 等网段，容器源 IP 不是 docker0 网关本身，
  # 所以 pg_hba 必须放 172.16.0.0/12 + 10.0.0.0/8 这两个 Docker 常用大段，仅放单个 /32 会 28000 连不上。
  PG_CONF="$(sudo -u postgres psql -tAc 'SHOW config_file;' | tr -d ' ')"
  PG_DIR="$(dirname "$PG_CONF")"
  LISTEN_CHANGED=0
  if ! grep -qE "^\s*listen_addresses.*${DOCKER_GW}" "$PG_DIR/postgresql.conf" 2>/dev/null; then
    cp "$PG_DIR/postgresql.conf" "$PG_DIR/postgresql.conf.bak.$(date +%F)" 2>/dev/null || true
    if grep -qE "^\s*listen_addresses" "$PG_DIR/postgresql.conf"; then
      sed -i "s/^\s*listen_addresses.*/listen_addresses = '127.0.0.1,${DOCKER_GW}'/" "$PG_DIR/postgresql.conf"
    else
      sed -i "s/^#listen_addresses.*/listen_addresses = '127.0.0.1,${DOCKER_GW}'/" "$PG_DIR/postgresql.conf"
    fi
    LISTEN_CHANGED=1
  fi
  HBA_CHANGED=0
  for net in '172.16.0.0/12' '10.0.0.0/8'; do
    for db in "$PG_DB" "$PG_LOG_DB"; do
      if ! grep -qE "host\s+$db\s+$PG_USER\s+$net" "$PG_DIR/pg_hba.conf" 2>/dev/null; then
        [[ "$HBA_CHANGED" == "0" ]] && cp "$PG_DIR/pg_hba.conf" "$PG_DIR/pg_hba.conf.bak.$(date +%F)" 2>/dev/null || true
        printf 'host\t%s\t%s\t%s\tscram-sha-256\n' "$db" "$PG_USER" "$net" >> "$PG_DIR/pg_hba.conf"
        HBA_CHANGED=1
      fi
    done
  done
  if [[ "$LISTEN_CHANGED" == "1" ]]; then
    # listen_addresses 改动必须 restart，reload 不生效
    log "postgresql.conf listen 改动，重启 postgresql…"
    systemctl restart postgresql 2>/dev/null || service postgresql restart || true
    sleep 2
  elif [[ "$HBA_CHANGED" == "1" ]]; then
    sudo -u postgres psql -c "SELECT pg_reload_conf();" >/dev/null
  fi

  # redis：带密码鉴权读配置；配置文件按进程命令行/常见路径探测
  REDIS_CLI=(redis-cli -p "${REDIS_PORT}")
  [[ -n "${REDIS_PASSWORD:-}" ]] && REDIS_CLI+=( -a "$REDIS_PASSWORD" --no-auth-warning )
  RCONF="$("${REDIS_CLI[@]}" --raw CONFIG GET bind 2>/dev/null | tail -1 || true)"
  if [[ "$RCONF" != *"$DOCKER_GW"* ]]; then
    REDIS_CONF=""
    for cand in $(ps -o args= -C redis-server 2>/dev/null | grep -oE '/[^ ]*\.conf' | head -3) /etc/redis/redis.conf /etc/redis.conf; do
      [[ -f "$cand" ]] && { REDIS_CONF="$cand"; break; }
    done
    [[ -z "$REDIS_CONF" ]] && { echo "找不到 redis.conf，跳过 bind 修改"; }
    if [[ -n "$REDIS_CONF" ]]; then
      cp "$REDIS_CONF" "$REDIS_CONF.bak.$(date +%F)" 2>/dev/null || true
      sed -i "s/^bind .*/bind 127.0.0.1 ::1 ${DOCKER_GW}/" "$REDIS_CONF"
      systemctl restart redis-server 2>/dev/null || service redis-server restart || true
      sleep 2
    fi
  fi

  # --- 5d. 组装 DSN 写回（密码 URL 编码，特殊字符不炸） ---
  PP_ENC="$(urlencode "$PG_PASSWORD")"
  SQL_DSN="postgres://${PG_USER}:${PP_ENC}@host.docker.internal:${PG_PORT}/${PG_DB}?sslmode=disable"
  LOG_SQL_DSN="postgres://${PG_USER}:${PP_ENC}@host.docker.internal:${PG_PORT}/${PG_LOG_DB}?sslmode=disable"
  if [[ -n "${REDIS_PASSWORD:-}" ]]; then
    RP_ENC="$(urlencode "$REDIS_PASSWORD")"
    REDIS_CONN_STRING="redis://:${RP_ENC}@host.docker.internal:${REDIS_PORT}/${REDIS_DB}"
  else
    REDIS_CONN_STRING="redis://host.docker.internal:${REDIS_PORT}/${REDIS_DB}"
  fi
  export SQL_DSN LOG_SQL_DSN REDIS_CONN_STRING
  env_set_dsns "$SQL_DSN" "$LOG_SQL_DSN" "$REDIS_CONN_STRING"
  COMPOSE_FILES="-f docker-compose.yml"
else
  # --- bundled：compose 顺手起 pg+redis 容器 ---
  # 注意：postgres 镜像只自动建 POSTGRES_DB 那一个库，LOG_SQL_DSN 留空=复用主库建 log 表；
  # 若指向不存在的 new-api-log 库会导致启动连库失败，所以这里故意留空。
  [[ -z "${POSTGRES_PASSWORD:-}" ]] && { POSTGRES_PASSWORD="$(rand_hex 16)"; export POSTGRES_PASSWORD; env_set POSTGRES_PASSWORD "$POSTGRES_PASSWORD"; }
  [[ -z "${REDIS_PASSWORD_BUNDLED:-}" ]] && { REDIS_PASSWORD_BUNDLED="$(rand_hex 16)"; export REDIS_PASSWORD_BUNDLED; env_set REDIS_PASSWORD_BUNDLED "$REDIS_PASSWORD_BUNDLED"; }
  PP_ENC="$(urlencode "$POSTGRES_PASSWORD")"
  SQL_DSN="postgres://postgres:${PP_ENC}@postgres:5432/new-api?sslmode=disable"
  LOG_SQL_DSN=""
  RP_ENC="$(urlencode "$REDIS_PASSWORD_BUNDLED")"
  REDIS_CONN_STRING="redis://:${RP_ENC}@redis:6379/0"
  export SQL_DSN LOG_SQL_DSN REDIS_CONN_STRING
  env_set_dsns "$SQL_DSN" "$LOG_SQL_DSN" "$REDIS_CONN_STRING"
  COMPOSE_FILES="-f docker-compose.bundled.yml"
fi

# ---------- 6. 起服 ----------
cd "$DEPLOY_DIR"
# shellcheck disable=SC2086
docker compose --env-file "$ENV_FILE" $COMPOSE_FILES up -d
log "等待健康检查…"
for i in $(seq 1 30); do
  if curl -sf "http://127.0.0.1:${APP_PORT}/api/status" | grep -q '"success": *true'; then
    log "部署成功: http://$(hostname -I | awk '{print $1}'):3000 (源码 $SHORT_SHA)"
    echo "首次使用请初始化管理员: curl -X POST http://127.0.0.1:${APP_PORT}/api/user/setup -H 'Content-Type: application/json' -d '{\"username\":\"admin\",\"password\":\"<密码>\",\"confirmPassword\":\"<密码>\"}'"
    exit 0
  fi
  sleep 5
done
echo "启动超时，看日志: cd $DEPLOY_DIR && docker compose logs --tail=100"
exit 1
