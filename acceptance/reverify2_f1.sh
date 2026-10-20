set -eu
cd /volume1/docker/seltool-f1-work
python3 - <<'PY'
import json
r=json.load(open('parity-results.json'))
for x in r: print(x['name']+':',x['result'])
PY
echo "=== 资源拦截（PNG拦截+WebP放行，真实Chromium）==="
grep -q 'PASS: TestF1BrowserResourcesAndStorage' browser-integration.log && echo "资源拦截: OK" || echo "资源拦截: MISSING"
echo "=== 启动失败通知（配置损坏+浏览器故障，真实Webhook）==="
docker exec sel-candidate-mock python - <<'PY'
import json,urllib.request
d=json.load(urllib.request.urlopen('http://mock.test:8000/admin/state'))
ev=[x['text'] for x in d['events'] if '启动失败' in x['text']]
print('配置损坏通知:', 'OK' if any('目标配置' in x for x in ev) else 'MISSING')
print('浏览器失败通知:', 'OK' if any('浏览器' in x for x in ev) else 'MISSING')
PY
echo "=== VNC 协议（认证/像素/重连，进程内客户端）==="
docker exec sel-f1-app /test/vnctest -addr 127.0.0.1:7900 -pass fixture1 -out /tmp/f1-vnc-verify >/dev/null && echo "VNC协议: OK" || echo "VNC协议: MISSING"
echo "=== 全部完成 ==="
