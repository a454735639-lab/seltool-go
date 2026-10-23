#!/bin/sh
# 第二轮裁剪（修正版）：只做经二分实测确认安全的两项。
#   A. Vulkan 校验层 + mock ICD —— 实测 x11vnc OK、WebGL OK
#   B. GTK emoji 多余语言 —— 实测 x11vnc OK
# 键盘布局经实测「删了 x11vnc 起不来」，故不动。
set -eu

SRC=seltool-go:runtime-slim
OUT=seltool-go:runtime-slim2
TMPTAR=/volume1/docker/seltool-f1-work/slim2.tar

docker rm -f slim2 >/dev/null 2>&1 || true
docker run -d --name slim2 --entrypoint sleep "$SRC" infinity >/dev/null

before=$(docker exec slim2 du -sm / 2>/dev/null | awk '{print $1}')
echo "文件系统（裁剪前）: ${before}MB"

echo '--- A. 删除 Vulkan 校验层与 mock ICD ---'
docker exec slim2 sh -c '
for f in libVkLayer_khronos_validation.so libVkLayer_khronos_validation.so.TOC \
         libVkICD_mock_icd.so libVkICD_mock_icd.so.TOC; do
  p=/usr/lib/chromium/$f
  [ -f "$p" ] && { sz=$(du -k "$p" | cut -f1); rm -f "$p"; echo "  删除 $f (${sz}KB)"; }
done'

echo '--- B. 删除 GTK emoji 非必要语言 ---'
docker exec slim2 sh -c '
cd /usr/share/gtk-3.0/emoji 2>/dev/null || exit 0
for f in *.gresource; do
  case "$f" in
    de.gresource|es.gresource|fr.gresource|zh.gresource) ;;
    *) sz=$(du -k "$f" | cut -f1); rm -f "$f"; echo "  删除 $f (${sz}KB)" ;;
  esac
done'

after=$(docker exec slim2 du -sm / 2>/dev/null | awk '{print $1}')
echo "文件系统（裁剪后）: ${after}MB"

echo
echo '══ 完整组件复验（用生产同款 RFB 探针）══'
docker exec slim2 sh -c '
rm -f /tmp/.X99-lock; Xvfb :99 -screen 0 1280x720x24 -nolisten tcp -ac >/tmp/x.log 2>&1 & sleep 3
[ -S /tmp/.X11-unix/X99 ] && echo "Xvfb OK" || echo "Xvfb FAIL"
DISPLAY=:99 fluxbox >/tmp/fb.log 2>&1 & sleep 2
pgrep -x fluxbox >/dev/null && echo "fluxbox OK" || echo "fluxbox FAIL"
x11vnc -storepasswd t /tmp/vp >/dev/null 2>&1
DISPLAY=:99 x11vnc -display :99 -forever -shared -rfbport 5900 -rfbauth /tmp/vp -quiet -noxdamage >/tmp/v.log 2>&1 &
i=0
while [ $i -lt 15 ]; do
  /app/vncproxy -tcp-probe 127.0.0.1:5900 -tcp-probe-timeout 1s >/dev/null 2>&1 && { echo "x11vnc OK（RFB 探针通过）"; break; }
  i=$((i+1)); sleep 1
done
[ $i -ge 15 ] && { echo "x11vnc FAIL"; tail -4 /tmp/v.log; }
/usr/bin/chromium --version 2>&1 | head -1
DISPLAY=:99 chromium --no-sandbox --disable-gpu --headless=new --dump-dom "data:text/html,<script>var c=document.createElement(\"canvas\");var g=c.getContext(\"webgl\")||c.getContext(\"experimental-webgl\");document.title=g?\"WEBGL_OK\":\"WEBGL_NONE\";</script>" 2>/dev/null | grep -o "WEBGL_[A-Z]*" | head -1'

echo
echo '══ 导出压平 ══'
docker export slim2 -o "$TMPTAR"
echo "tar: $(du -sm "$TMPTAR" | awk '{print $1}')MB"
docker import \
  -c 'ENTRYPOINT ["/usr/bin/tini","--","/app/entrypoint.sh"]' \
  -c 'WORKDIR /app' \
  -c 'ENV DISPLAY=:99 TZ=Asia/Shanghai SELTOOL_CHROME_BIN=/usr/bin/chromium DEBIAN_FRONTEND=noninteractive PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin' \
  -c 'EXPOSE 7900 8080' \
  "$TMPTAR" "$OUT" >/dev/null
docker rm -f slim2 >/dev/null
rm -f "$TMPTAR"

echo
echo '══ 结果 ══'
docker images --format "{{.Repository}}:{{.Tag}} {{.ID}} {{.Size}}" | grep -E 'seltool-go:(runtime-slim|runtime-slim2|candidate-safe)'
