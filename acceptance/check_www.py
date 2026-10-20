import json, subprocess, urllib.request

cfg = json.loads(subprocess.run(["docker","exec","seltool-go","cat","/app/config/cookiecloud-settings.json"],
                                capture_output=True, text=True).stdout)
url, key, pw = cfg["url"].rstrip("/"), cfg["key"], cfg["password"]
ep = url + "/get/" + key if url.endswith("/cookiecloud") else url + "/cookiecloud/get/" + key
req = urllib.request.Request(ep, data=json.dumps({"password": pw}).encode(),
                             headers={"Content-Type": "application/json"})
data = json.load(urllib.request.urlopen(req, timeout=40))["cookie_data"]
UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36"

for host in ["www.gamegamept.com", "www.qingwapt.com"]:
    cks = []
    for dom, lst in data.items():
        if dom.lstrip(".").lower() == host:
            cks += lst
    if not cks:
        print(host, "无精确域 Cookie")
        continue
    hdr = "; ".join(c["name"] + "=" + c["value"] for c in cks)
    out = subprocess.run(["curl", "-s", "-m", "20", "-L", "-o", "/tmp/w.html",
                          "-w", "%{http_code} %{url_effective}", "-A", UA,
                          "-H", "Cookie: " + hdr, "https://" + host + "/"],
                         capture_output=True, text=True)
    body = open("/tmp/w.html", errors="ignore").read()
    logged = ("logout.php" in body) or ("usercp.php" in body)
    form = "takelogin.php" in body
    print(f"{host:24s} {out.stdout} cookies={len(cks)} 已登录={logged} 登录表单={form}")
