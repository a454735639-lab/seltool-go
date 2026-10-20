#!/usr/bin/env python3
"""只读诊断：把 CookieCloud 里的 Cookie 直接用 curl 打到站点，判断失败是站点侧还是注入侧。
不打印任何 Cookie 值/密码/key。"""
import json, subprocess, sys, urllib.request, urllib.error

SETTINGS_CMD = ['docker', 'exec', 'seltool-go', 'cat', '/app/config/cookiecloud-settings.json']
raw = subprocess.run(SETTINGS_CMD, capture_output=True, text=True).stdout
cfg = json.loads(raw)
url, key, pw = cfg['url'].rstrip('/'), cfg['key'], cfg['password']
endpoint = url + '/get/' + key if url.endswith('/cookiecloud') else url + '/cookiecloud/get/' + key
req = urllib.request.Request(endpoint, data=json.dumps({'password': pw}).encode(),
                             headers={'Content-Type': 'application/json'})
payload = json.load(urllib.request.urlopen(req, timeout=40))
data = payload.get('cookie_data', {})

hosts = sys.argv[1:]
UA = 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36'

for host in hosts:
    cookies = []
    for dom, lst in data.items():
        d = dom.lstrip('.').lower()
        if host == d or host.endswith('.' + d) or d.endswith('.' + host):
            cookies.extend(lst)
    if not cookies:
        print(f'{host:22s} CookieCloud 无该域 Cookie → 数据缺失')
        continue
    hdr = '; '.join(f"{c['name']}={c['value']}" for c in cookies)
    out = subprocess.run(['curl', '-s', '-m', '20', '-L', '-o', '/tmp/diag.html',
                          '-w', '%{http_code} %{url_effective}', '-A', UA,
                          '-H', f'Cookie: {hdr}', f'https://{host}/'],
                         capture_output=True, text=True)
    body = ''
    try:
        body = open('/tmp/diag.html', errors='ignore').read()
    except Exception:
        pass
    logged = ('logout.php' in body) or ('usercp.php' in body)
    loginform = 'takelogin.php' in body
    verdict = '站点侧：Cookie 已失效/不完整（需重新登录并同步）' if not logged else '注入侧：Cookie 本身有效，需查注入'
    print(f'{host:22s} HTTP {out.stdout.split()[0] if out.stdout else "---":>4s} '
          f'cookies={len(cookies):2d} 已登录={logged!s:5s} 登录表单={loginform!s:5s} → {verdict}')
