#!/bin/sh
# 构建并部署 seltool-go。
#
# 用法（在 NAS 上）：
#   sh scripts/build.sh              # 编译 + 构建镜像 + 部署
#   sh scripts/build.sh --no-deploy  # 只编译和构建，不重启容器
#   sh scripts/build.sh --base       # 额外重建基础镜像 runtime-slim2
#
# 背景：基础镜像 runtime-slim2 被误删过两次（docker image prune -af 会删掉
# 所有「未被容器使用」的镜像，包括它），导致构建报 pull access denied。
# 本脚本第一步就检查它，缺了会给出明确提示。

set -eu

WORK=${WORK_DIR:-/volume1/docker/seltool-f1-work}
DEPLOY=${DEPLOY_DIR:-/volume1/docker/seltool-go}
BASE_IMAGE=seltool-go:runtime-slim2
IMAGE=seltool-go:candidate-safe
GO_IMAGE=golang:1.26-alpine

DEPLOY_ENABLED=1
REBUILD_BASE=0
for arg in "$@"; do
  case "$arg" in
    --no-deploy) DEPLOY_ENABLED=0 ;;
    --base)      REBUILD_BASE=1 ;;
    *) echo "未知参数: $arg"; exit 2 ;;
  esac
done

log() { printf '\033[36m[build]\033[0m %s\n' "$*"; }
die() { printf '\033[31m[build] 失败：%s\033[0m\n' "$*" >&2; exit 1; }

# ── 0. 基础镜像检查 ──
if [ "$REBUILD_BASE" = "1" ]; then
  log "重建基础镜像 $BASE_IMAGE（从当前生产镜像剥离业务文件）"
  docker rm -f tmpbase >/dev/null 2>&1 || true
  docker run -d --name tmpbase --entrypoint sleep "$IMAGE" infinity >/dev/null
  docker exec tmpbase rm -f /app/seltool /app/vncproxy /app/entrypoint.sh
  docker export tmpbase | docker import - "$BASE_IMAGE" \
    --change 'ENTRYPOINT ["/usr/bin/tini","--","/app/entrypoint.sh"]' >/dev/null
  docker rm -f tmpbase >/dev/null
  log "基础镜像已重建"
fi

if ! docker image inspect "$BASE_IMAGE" >/dev/null 2>&1; then
  cat >&2 <<EOF

基础镜像 $BASE_IMAGE 不存在。

它被 docker image prune -af 删除过（-a 会删所有未使用镜像，
包括「没有容器直接引用」但被 Dockerfile FROM 引用的基础镜像）。

恢复方式（二选一）：
  sh scripts/build.sh --base      # 从当前生产镜像剥离业务文件重建
  docker pull ...                 # 或从别处导入

EOF
  exit 1
fi
log "基础镜像 OK：$(docker images --format '{{.Size}}' "$BASE_IMAGE" | head -1)"

# ── 0.5 前端静态校验 ──
# 历史教训：手工改 CSS/JS 出过三次事故（正则吞规则、删函数留调用、
# details 被 fixed），都能静态发现，所以放在编译之前。
cd "$WORK"
log "静态校验 web/index.html"
python3 src/scripts/check-frontend.py src/web/index.html || die "前端静态校验未通过"

# ── 1. 编译 ──
log "编译 Go 二进制"
docker run --rm -e GOFLAGS=-mod=mod -e CGO_ENABLED=0 \
  -v "$PWD/src:/src" -w /src "$GO_IMAGE" sh -ec "
    gofmt -w *.go
    go vet ./... 2>&1 | head -5 || true
    go build -o /src/seltool .
    go build -o /src/vncproxy ./cmd/vncproxy
  " || die "编译失败"
log "编译完成，测试"
docker run --rm -e GOFLAGS=-mod=mod -e CGO_ENABLED=0 \
  -v "$PWD/src:/src" -w /src "$GO_IMAGE" sh -ec "go test -short ./... 2>&1 | tail -3" \
  || die "测试失败"

cp src/seltool src/vncproxy image/
[ -f src/deploy/entrypoint.sh ] && cp src/deploy/entrypoint.sh image/deploy/entrypoint.sh

# ── 2. 构建镜像 ──
log "构建 $IMAGE"
DOCKER_BUILDKIT=0 docker build --pull=false \
  -f image/deploy/Dockerfile.f1 -t "$IMAGE" image > /tmp/build-$$.log 2>&1 \
  || { tail -8 /tmp/build-$$.log >&2; die "镜像构建失败"; }
log "镜像构建完成：$(docker images --format '{{.Size}}' "$IMAGE" | head -1)"

if [ "$DEPLOY_ENABLED" = "0" ]; then
  log "--no-deploy，跳过部署"
  exit 0
fi

# ── 3. 部署 ──
log "部署"
cd "$DEPLOY"
NEW=$(docker image inspect "$IMAGE" --format '{{.Id}}')
[ -f docker-compose.yml.bak-build ] || cp docker-compose.yml docker-compose.yml.bak-build
sed -i "s|image: sha256:.*|image: $NEW|" docker-compose.yml
docker compose up -d --no-build --force-recreate 2>&1 | tail -1

# ── 4. 验证 ──
log "等待启动"
for i in $(seq 1 20); do
  sleep 1
  if curl -sf -m 3 --noproxy '*' -o /dev/null http://127.0.0.1:8081/login.html; then
    log "控制台就绪（${i}s）"
    break
  fi
  [ "$i" = "20" ] && die "控制台 20 秒内未就绪"
done

curl -sf -m 5 --noproxy '*' -o /dev/null http://127.0.0.1:7900/ \
  && log "noVNC 就绪" || log "警告：noVNC 未响应"

log "完成。镜像 $(docker images --format '{{.ID}}' "$IMAGE" | head -1 | cut -c1-12) / $(docker images --format '{{.Size}}' "$IMAGE" | head -1)"
