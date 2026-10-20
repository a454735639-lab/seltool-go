#!/bin/sh
# 对当前生产镜像做一次严格的空间审计，量化每一块可回收空间。
set -eu
IMG=32eb50fe3d09

run() { docker run --rm --entrypoint sh "$IMG" -c "$1"; }

echo '══ 1. 顶层目录 ══'
run 'du -sm /usr /var /opt /etc /root /tmp /app 2>/dev/null | sort -rn'

echo
echo '══ 2. /usr 细分 ══'
run 'du -sm /usr/lib /usr/share /usr/bin /usr/libexec /usr/lib/aarch64-linux-gnu /usr/lib/chromium 2>/dev/null | sort -rn'

echo
echo '══ 3. 剩余体积最大的 25 个包 ══'
run 'dpkg-query -W -f="\${Installed-Size}|\${Package}\n" | sort -t"|" -k1 -rn | head -25 | awk -F"|" "{printf \"%8.1f MB  %s\n\", \$1/1024, \$2}"'

echo
echo '══ 4. chromium 目录构成 ══'
run 'du -sm /usr/lib/chromium/* 2>/dev/null | sort -rn | head -16 | awk "{printf \"%7.1f MB  %s\n\", \$1, \$2}"'

echo
echo '══ 5. chromium 内可回收项 ══'
run 'echo "locales:"; du -sm /usr/lib/chromium/locales 2>/dev/null; ls /usr/lib/chromium/locales 2>/dev/null | wc -l; echo "--- 实际使用的 locale（只保留 zh-CN/en-US）---"; du -sm /usr/lib/chromium/locales/*.pak 2>/dev/null | sort -rn | head -4'

echo
echo '══ 6. 字体 ══'
run 'du -sm /usr/share/fonts/* 2>/dev/null | sort -rn | head -8'

echo
echo '══ 7. noVNC 资源 ══'
run 'du -sm /usr/share/novnc 2>/dev/null; du -sm /usr/share/novnc/* 2>/dev/null | sort -rn | head -8'

echo
echo '══ 8. 共享库依赖检查（确认哪些库真被用到）══'
run 'ldd /usr/bin/chromium 2>/dev/null | grep "=>" | awk "{print \$3}" | xargs -I{} sh -c "dpkg -S {} 2>/dev/null | cut -d: -f1" 2>/dev/null | sort -u | head -40'

echo
echo '══ 9. 残留垃圾 ══'
run 'du -sm /var/cache /var/lib/apt /var/log /usr/share/doc /usr/share/man /usr/share/locale /tmp 2>/dev/null'

echo
echo '══ 10. 可能未使用的 Mesa/GL 后端 ══'
run 'ls -la /usr/lib/aarch64-linux-gnu/dri/ 2>/dev/null | head -8; du -sm /usr/lib/aarch64-linux-gnu/dri 2>/dev/null'
