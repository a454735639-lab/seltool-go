#!/bin/sh
# 二分定位：三组删除各自对 x11vnc 的影响。
set -eu

probe() {
  docker run --rm --entrypoint sh "$1" -c "
rm -f /tmp/.X99-lock; Xvfb :99 -screen 0 1280x720x24 -nolisten tcp -ac >/tmp/x.log 2>&1 & sleep 3
x11vnc -storepasswd t /tmp/vp >/dev/null 2>&1
DISPLAY=:99 x11vnc -display :99 -forever -shared -rfbport 5900 -rfbauth /tmp/vp -quiet -noxdamage >/tmp/v.log 2>&1 &
i=0
while [ \$i -lt 12 ]; do
  /app/vncproxy -tcp-probe 127.0.0.1:5900 -tcp-probe-timeout 1s >/dev/null 2>&1 && { echo OK; exit 0; }
  i=\$((i+1)); sleep 1
done
echo FAIL
"
}

echo '=== 基线 runtime-slim ==='
probe seltool-go:runtime-slim

echo '=== A: 只删 Vulkan 校验层 + mock ICD ==='
docker rm -f bisA >/dev/null 2>&1 || true
docker run -d --name bisA --entrypoint sleep seltool-go:runtime-slim infinity >/dev/null
docker exec bisA sh -c 'rm -f /usr/lib/chromium/libVkLayer_khronos_validation.so /usr/lib/chromium/libVkLayer_khronos_validation.so.TOC /usr/lib/chromium/libVkICD_mock_icd.so /usr/lib/chromium/libVkICD_mock_icd.so.TOC'
docker commit bisA seltool-go:bisA >/dev/null && docker rm -f bisA >/dev/null
probe seltool-go:bisA

echo '=== B: 只删键盘布局 ==='
docker rm -f bisB >/dev/null 2>&1 || true
docker run -d --name bisB --entrypoint sleep seltool-go:runtime-slim infinity >/dev/null
docker exec bisB sh -c '
cd /usr/share/X11/xkb/symbols || exit 0
keep="us gb de fr cn ru jp kr es it pt br nl se no dk fi pl cz hu tr gr il in th vn"
for f in *; do case " $keep " in *" $f "*) ;; *) rm -f "$f" 2>/dev/null || true ;; esac; done'
docker commit bisB seltool-go:bisB >/dev/null && docker rm -f bisB >/dev/null
probe seltool-go:bisB

echo '=== C: 只删 GTK emoji ==='
docker rm -f bisC >/dev/null 2>&1 || true
docker run -d --name bisC --entrypoint sleep seltool-go:runtime-slim infinity >/dev/null
docker exec bisC sh -c '
cd /usr/share/gtk-3.0/emoji 2>/dev/null || exit 0
for f in *.gresource; do case "$f" in de.gresource|es.gresource|fr.gresource|zh.gresource) ;; *) rm -f "$f" ;; esac; done'
docker commit bisC seltool-go:bisC >/dev/null && docker rm -f bisC >/dev/null
probe seltool-go:bisC

echo '=== 清理 ==='
docker rmi seltool-go:bisA seltool-go:bisB seltool-go:bisC >/dev/null 2>&1 || true
