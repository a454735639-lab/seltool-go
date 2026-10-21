#!/bin/sh
# 一键发布：推送源码到 NAS 构建部署，成功后备份到 Obsidian。
#
# 在 iSH 上跑（因为 Obsidian 挂载点在这边，NAS 看不到）：
#   sh scripts/deploy-and-backup.sh              # 构建 + 部署 + 备份
#   sh scripts/deploy-and-backup.sh --no-deploy  # 只构建，不重启容器
#   SKIP_BACKUP=1 sh scripts/deploy-and-backup.sh
#
# 依赖环境变量：NAS_SSH_USER / NAS_SSH_PASSWORD

set -eu

NAS=${NAS_HOST:?请先设置 NAS_HOST（NAS 的 IP 或主机名）}
WORK=${NAS_WORK:-/volume1/docker/seltool-f1-work/src}
HERE=$(cd "$(dirname "$0")/.." && pwd)

[ -n "${NAS_SSH_USER:-}" ] || { echo "缺少 NAS_SSH_USER" >&2; exit 1; }
[ -n "${NAS_SSH_PASSWORD:-}" ] || { echo "缺少 NAS_SSH_PASSWORD" >&2; exit 1; }

ARGS=""
for a in "$@"; do
  case "$a" in
    --no-deploy) ARGS="$ARGS --no-deploy" ;;
    *) echo "未知参数: $a" >&2; exit 2 ;;
  esac
done

log() { printf '\033[36m[deploy]\033[0m %s\n' "$*"; }

cd "$HERE"

# ── 1. 本地先跑一遍静态校验，避免把明显坏的东西传上去 ──
log "本地静态校验"
python3 scripts/check-frontend.py web/index.html || { echo "校验未通过，已中止" >&2; exit 1; }

# ── 2. 推送源码到 NAS 并构建 ──
log "推送源码到 $NAS"
tar czf - --exclude='.git' --exclude='data' --exclude='config' --exclude='*.log' . \
  | sshpass -p "$NAS_SSH_PASSWORD" ssh -o ConnectTimeout=20 -o StrictHostKeyChecking=no \
      "$NAS_SSH_USER@$NAS" "tar xzf - -C '$WORK' && cd '$WORK' && sh scripts/build.sh$ARGS" \
  || { echo "构建或部署失败" >&2; exit 1; }

# ── 3. 备份到 Obsidian ──
if [ "${SKIP_BACKUP:-0}" = "1" ]; then
  log "SKIP_BACKUP=1，跳过备份"
  exit 0
fi

if [ -d /var/minis/mounts/obsidian/Minis ]; then
  log "备份到 Obsidian"
  sh scripts/backup-obsidian.sh
else
  log "Obsidian 未挂载，跳过备份"
fi
