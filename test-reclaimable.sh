#!/bin/sh
# 实测最大可回收项：Vulkan 软渲染库对 WebGL / 页面渲染是否必需。
set -eu
IMG=32eb50fe3d09

docker rm -f vk-test >/dev/null 2>&1 || true
docker run -d --name vk-test --entrypoint sleep "$IMG" infinity >/dev/null

echo '══ 基线：未删库时的 WebGL 能力 ══'
docker exec vk-test sh -c 'rm -f /tmp/.X99-lock; Xvfb :99 -screen 0 1280x720x24 -nolisten tcp -ac >/tmp/x.log 2>&1 & sleep 3; DISPLAY=:99 chromium --no-sandbox --disable-gpu --headless=new --dump-dom "data:text/html,<script>var c=document.createElement(\"canvas\");var g=c.getContext(\"webgl\")||c.getContext(\"experimental-webgl\");document.title=g?\"WEBGL_OK:\"+g.getParameter(g.VERSION):\"WEBGL_NONE\";</script>" 2>/dev/null | head -c 200'

echo
echo '══ 测试 1：删掉 libvk_swiftshader.so（27MB）══'
docker exec vk-test sh -c 'mv /usr/lib/chromium/libvk_swiftshader.so /tmp/ 2>/dev/null || true'
docker exec vk-test sh -c 'DISPLAY=:99 chromium --no-sandbox --disable-gpu --headless=new --dump-dom "data:text/html,<script>var c=document.createElement(\"canvas\");var g=c.getContext(\"webgl\")||c.getContext(\"experimental-webgl\");document.title=g?\"WEBGL_OK\":\"WEBGL_NONE\";</script>" 2>/dev/null | head -c 200'
docker exec vk-test sh -c 'echo "--- 启动错误检查 ---"; DISPLAY=:99 chromium --no-sandbox --headless=new --dump-dom about:blank 2>&1 >/dev/null | grep -icE "swiftshader|libvk" || echo 0'
docker exec vk-test sh -c 'mv /tmp/libvk_swiftshader.so /usr/lib/chromium/ 2>/dev/null || true'

echo
echo '══ 测试 2：再删 libVkLayer_khronos_validation.so（22MB）══'
docker exec vk-test sh -c 'mv /usr/lib/chromium/libVkLayer_khronos_validation.so /tmp/ 2>/dev/null || true'
docker exec vk-test sh -c 'DISPLAY=:99 chromium --no-sandbox --disable-gpu --headless=new --dump-dom "data:text/html,<script>var c=document.createElement(\"canvas\");var g=c.getContext(\"webgl\")||c.getContext(\"experimental-webgl\");document.title=g?\"WEBGL_OK\":\"WEBGL_NONE\";</script>" 2>/dev/null | head -c 200'
docker exec vk-test sh -c 'DISPLAY=:99 chromium --no-sandbox --headless=new --dump-dom about:blank 2>&1 >/dev/null | grep -icE "VkLayer|validation" || echo 0'

echo
echo '══ 测试 3：coreutils 是否可换 busybox ══'
docker exec vk-test sh -c 'echo "coreutils 被谁依赖:"; apt-cache rdepends --installed coreutils 2>/dev/null | tail -n +3 | tr -d " " | head -8'

echo
echo '══ 测试 4：libgtk-3-common 内容 ══'
docker exec vk-test sh -c 'du -sm /usr/share/gtk-doc /usr/share/themes /usr/share/icons /usr/lib/aarch64-linux-gnu/gtk-3.0 2>/dev/null | sort -rn | head -6'

echo
echo '══ 测试 5：entrypoint 用 bash 的哪些特性 ══'
docker exec vk-test sh -c 'grep -cE "\\[\\[|\\$\\{|local |function |\\+\\+|&>|\\[\\[" /app/entrypoint.sh 2>/dev/null || echo 0; head -1 /app/entrypoint.sh'

docker rm -f vk-test >/dev/null
echo
echo '══ 完成 ══'
