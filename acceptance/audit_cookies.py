import json, subprocess, urllib.request
from urllib.parse import urlparse

cfg = json.loads(subprocess.run(["docker","exec","seltool-go","cat","/app/config/cookiecloud-settings.json"],
                                capture_output=True, text=True).stdout)
url, key, pw = cfg["url"].rstrip("/"), cfg["key"], cfg["password"]
ep = url + "/get/" + key if url.endswith("/cookiecloud") else url + "/cookiecloud/get/" + key
req = urllib.request.Request(ep, data=json.dumps({"password": pw}).encode(),
                             headers={"Content-Type": "application/json"})
data = json.load(urllib.request.urlopen(req, timeout=60))["cookie_data"]

targets = json.loads(subprocess.run(["docker","exec","seltool-go","cat","/app/config/targets.json"],
                                    capture_output=True, text=True).stdout)

rows = []
for t in targets:
    host = (urlparse(t["url"]).hostname or "").lower()
    direct, subs = 0, {}
    for dom, lst in data.items():
        d = dom.lstrip(".").lower()
        if d == host or host.endswith("." + d):
            direct += len(lst)
        elif d.endswith("." + host):
            subs[d] = subs.get(d, 0) + len(lst)
    if direct:
        state = f"直连 {direct}"
    elif len(subs) == 1:
        only = list(subs)[0]
        state = f"仅子域 {only}（{subs[only]}）→ 自动切换"
    elif len(subs) > 1:
        state = "多子域：" + ",".join(subs)
    else:
        state = "无 Cookie → 需重新登录同步"
    rows.append((host, state))

for host, state in sorted(rows, key=lambda r: (not r[1].startswith("无"), r[0])):
    print(f"{host:24s} {state}")
