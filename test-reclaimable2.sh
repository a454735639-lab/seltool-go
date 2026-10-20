#!/bin/sh
# 第二轮：确认 bash / ICU / GTK / coreutils 是否真的不可动。
set -eu
IMG=32eb50fe3d09
run() { docker run --rm --entrypoint sh "$IMG" -c "$1"; }

echo '══ bash 被谁依赖 ══'
run 'apt-cache rdepends --installed bash 2>/dev/null | tail -n +3 | tr -d " " | head -10'

echo
echo '══ entrypoint.sh 用了哪些 bash 专属特性 ══'
run 'grep -nE "pipefail|SECONDS|\\[\\[|BASH_|\\$\\{[A-Za-z_]+\\[|declare|local" /app/entrypoint.sh'

echo
echo '══ 若改用 sh，需要改掉哪几行 ══'
run 'grep -nE "pipefail|SECONDS" /app/entrypoint.sh'

echo
echo '══ libicu72 被谁依赖（ICU 是 chromium 硬依赖？）══'
run 'apt-cache rdepends --installed libicu72 2>/dev/null | tail -n +3 | tr -d " " | head -10'

echo
echo '══ libgtk-3-common 的 25.9MB 具体在哪 ══'
run 'dpkg -L libgtk-3-common | while read f; do [ -f "$f" ] && du -k "$f" 2>/dev/null; done | sort -rn | head -8'

echo
echo '══ libgtk-3-0 的 10.8MB 在哪 ══'
run 'dpkg -L libgtk-3-0 | while read f; do [ -f "$f" ] && du -k "$f" 2>/dev/null; done | sort -rn | head -6'

echo
echo '══ coreutils 20MB 具体文件 ══'
run 'dpkg -L coreutils | while read f; do [ -f "$f" ] && du -k "$f" 2>/dev/null; done | sort -rn | head -8'

echo
echo '══ xkb-data 6.8MB（X11 键盘布局，VNC 是否需要？）══'
run 'ls /usr/share/X11/xkb/symbols 2>/dev/null | wc -l'

echo
echo '══ 已装包总数与总体积 ══'
run 'dpkg-query -W -f="\${Installed-Size}\n" | awk "{s+=\$1} END {printf \"%d 个包，合计 %.0f MB\n\", NR, s/1024}"'
run 'echo "非包管理产生的文件（/usr 之外）:"; du -sm /app /etc /var /root 2>/dev/null | tail -5'
