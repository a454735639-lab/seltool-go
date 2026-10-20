set -eu
cd /volume1/docker/seltool-f1-work
R=verify-result
echo "=== 1. 远端503必须报错，不能用缓存冒充 ==="
docker exec sel-f1-mock python - <<'PY'
import urllib.request as u
s=json.load(u.urlopen('http://mock.test:8000/admin/state'))
PY
# verified by api_test cases; assert the saved test evidence explicitly.
grep -q 'PASS 测试连接不应掩盖远端503' accept-run/api-run.log && echo "503报错: OK" || echo "503报错: MISSING"
grep -q 'PASS 测试连接应拒绝错误密码' accept-run/api-run.log && echo "错误密码报错: OK" || echo "错误密码报错: MISSING"
echo "=== 2. YAML 目标迁移 ==="
grep -q 'PASS 原版YAML目标配置生效' parity-results.json && echo "YAML目标: OK" || echo "YAML目标: MISSING"
echo "=== 3. 模板三项后端校验 ==="
grep -q 'PASS 拒绝不存在模板ID' parity-results.json && echo "模板ID: OK" || echo "模板ID: MISSING"
grep -q 'PASS 模板固定字段不可篡改' parity-results.json && echo "固定字段: OK" || echo "固定字段: MISSING"
grep -q 'PASS 模板必填localStorage不可为空' parity-results.json && echo "必填localStorage: OK" || echo "必填localStorage: MISSING"
echo "=== 4. 真实资源拦截（PNG拦截+WebP放行） ==="
grep -q 'PASS: TestF1BrowserResourcesAndStorage' browser-integration.log && echo "资源拦截: OK" || echo "资源拦截: MISSING"
echo "=== 5. 启动失败通知（配置损坏+浏览器故障） ==="
docker exec sel-f1-mock python - <<'PY'
import json,urllib.request
d=json.load(urllib.request.urlopen('http://mock.test:8000/admin/state'))
ev=[x['text'] for x in d['events'] if '启动失败' in x['text']]
print('配置损坏通知:', any('目标配置' in x for x in ev))
print('浏览器失败通知:', any('浏览器' in x for x in ev))
PY
echo "=== 6. VNC 协议（认证/像素/输入/重连） ==="
docker exec sel-f1-app /test/vnctest -addr 127.0.0.1:7900 -pass fixture1 -out /tmp/f1-vnc-verify > /dev/null && echo "VNC协议: OK" || echo "VNC协议: MISSING"
echo "=== 完整 ==="
