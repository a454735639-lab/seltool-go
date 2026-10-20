#!/bin/sh
# 从当前生产镜像制作瘦身基础镜像 runtime-slim。
# 关键：必须基于「含全部最新功能」的生产镜像，而不是旧的 rollback。
set -eu

SRC=7b941df39107            # 当前生产镜像（定时任务 + UI 重构 + 通知模板全都在）
OUT=seltool-go:runtime-slim
TMPTAR=/volume1/docker/seltool-f1-work/slim-base.tar

# 只删「chromium 仅 Recommends / 间接依赖」的大包。
# 必须保留 libgl1、libglx0（Xvfb 硬依赖 libGL.so.1）与 libimlib2（fluxbox 硬依赖）。
PKGS="libllvm15 libz3-4 libgl1-mesa-dri \
libgs10 libgs10-common libspectre1 poppler-data fonts-urw-base35 \
adwaita-icon-theme systemd systemd-sysv libpam-systemd dbus-user-session"

echo "=== 源镜像 ==="
docker images --format "{{.Repository}}:{{.Tag}} {{.ID}} {{.Size}}" | grep -E "$SRC|slim-cand" || true

docker rm -f slim-base >/dev/null 2>&1 || true
docker run -d --name slim-base --entrypoint sleep "$SRC" infinity >/dev/null

before=$(docker exec slim-base du -sm / 2>/dev/null | awk '{print $1}')
echo "容器文件系统（移除前）: ${before}MB"

for p in $PKGS; do
  docker exec slim-base sh -c "dpkg --purge --force-all $p >/dev/null 2>&1 || true"
done
docker exec slim-base sh -c 'rm -rf /var/lib/apt/lists/* /var/cache/apt/* /usr/share/doc/* /usr/share/man/* /usr/share/locale/* 2>/dev/null || true'

after=$(docker exec slim-base du -sm / 2>/dev/null | awk '{print $1}')
echo "容器文件系统（移除后）: ${after}MB"

echo "=== 组件复验 ==="
docker exec slim-base sh -c '/usr/bin/chromium --version 2>&1 | head -1'
docker exec slim-base sh -c 'rm -f /tmp/.X99-lock; Xvfb :99 -screen 0 1280x720x24 -nolisten tcp -ac >/tmp/x.log 2>&1 & sleep 3; [ -S /tmp/.X11-unix/X99 ] && echo "Xvfb OK" || { echo "Xvfb FAIL"; tail -3 /tmp/x.log; }'
docker exec slim-base sh -c 'DISPLAY=:99 fluxbox >/tmp/fb.log 2>&1 & sleep 2; pgrep -x fluxbox >/dev/null && echo "fluxbox OK" || { echo "fluxbox FAIL"; tail -3 /tmp/fb.log; }'
docker exec slim-base sh -c 'x11vnc -storepasswd t /tmp/vp >/dev/null 2>&1; DISPLAY=:99 x11vnc -display :99 -forever -shared -rfbport 5900 -rfbauth /tmp/vp -quiet -noxdamage >/tmp/v.log 2>&1 & sleep 3; pgrep -x x11vnc >/dev/null && echo "x11vnc OK" || echo "x11vnc FAIL"'
docker exec slim-base sh -c '[ -x /app/seltool ] && echo "seltool OK" || echo "seltool MISS"'
docker exec slim-base sh -c '[ -x /app/vncproxy ] && echo "vncproxy OK" || echo "vncproxy MISS"'
docker exec slim-base sh -c '[ -x /usr/bin/tini ] && echo "tini OK" || echo "tini MISS"'

echo "=== 导出并压平 ==="
docker export slim-base -o "$TMPTAR"
echo "tar: $(du -sm "$TMPTAR" | awk '{print $1}')MB"

docker import \
  -c 'ENTRYPOINT ["/usr/bin/tini","--","/app/entrypoint.sh"]' \
  -c 'WORKDIR /app' \
  -c 'ENV DISPLAY=:99 TZ=Asia/Shanghai SELTOOL_CHROME_BIN=/usr/bin/chromium SE_VNC_PASSWORD=selenium DEBIAN_FRONTEND=noninteractive PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin' \
  -c 'EXPOSE 7900 8080' \
  "$TMPTAR" "$OUT" >/dev/null

docker rm -f slim-base >/dev/null
rm -f "$TMPTAR"

echo "=== 结果 ==="
docker images --format "{{.Repository}}:{{.Tag}} {{.ID}} {{.Size}}" | grep -E 'seltool-go:(runtime-slim|candidate-safe|slim-cand)'
