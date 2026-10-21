#!/usr/bin/env python3
"""在 NAS 本机验证定时任务接口全流程。"""
import json, os, sys, urllib.request, http.cookiejar

# 不要把账号密码写死在脚本里（曾经硬编码过，已从仓库移除）。
# 用法：SELTOOL_PASSWORD=xxx python3 verify_schedule.py
B = os.environ.get("SELTOOL_URL", "http://127.0.0.1:8081")
USER = os.environ.get("SELTOOL_USER", "admin")
PASSWORD = os.environ.get("SELTOOL_PASSWORD", "")
if not PASSWORD:
    sys.exit("请先设置 SELTOOL_PASSWORD 环境变量（面板登录密码）")
cj = http.cookiejar.CookieJar()
op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cj))

def req(path, method="GET", body=None, csrf=None, form=None):
    data = json.dumps(body).encode() if body is not None else (form.encode() if form else None)
    r = urllib.request.Request(B + path, data=data, method=method)
    if body is not None: r.add_header("Content-Type", "application/json")
    if form is not None: r.add_header("Content-Type", "application/x-www-form-urlencoded")
    if csrf: r.add_header("X-CSRF-TOKEN", csrf)
    try:
        with op.open(r, timeout=15) as resp:
            return resp.status, json.loads(resp.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode() or "{}")

st, d = req("/api/auth/status")
csrf = d["csrfToken"]
req("/api/auth/login", "POST", csrf=csrf, form=f"username={USER}&password={PASSWORD}")
st, d = req("/api/auth/status")
csrf = d["csrfToken"]

print("① 读取当前状态")
code, d = req("/api/settings/schedule")
print(f"   HTTP {code} → enabled={d.get('enabled')} cron={d.get('cron')}")
print(f"   describe={d.get('describe')}  nextRun={d.get('nextRun')}")

print("② 预设与时区")
code, d = req("/api/settings/schedule/presets")
print(f"   HTTP {code} → {len(d['presets'])} 个预设")
for p in d["presets"]:
    print(f"      {p['label']:14s} {p['cron']}")
print(f"   时区: {d['zones']}")

print("③ 改为每天 20:00")
code, d = req("/api/settings/schedule", "PUT", {"enabled": True, "cron": "0 0 20 * * *", "zone": "Asia/Shanghai"}, csrf)
print(f"   HTTP {code} → {d.get('message')} | {d.get('describe')} | 下次 {d.get('nextRun')}")

print("④ 非法 cron 应被拒")
code, d = req("/api/settings/schedule", "PUT", {"enabled": True, "cron": "not-a-cron", "zone": "Asia/Shanghai"}, csrf)
print(f"   HTTP {code} → {d.get('error')}")

print("⑤ 非法时区应被拒")
code, d = req("/api/settings/schedule", "PUT", {"enabled": True, "cron": "0 0 8 * * *", "zone": "Mars/Olympus"}, csrf)
print(f"   HTTP {code} → {d.get('error')}")

print("⑥ 非法请求后调度未被破坏（应仍是 20:00）")
code, d = req("/api/settings/schedule")
print(f"   HTTP {code} → cron={d.get('cron')}")

print("⑦ 停用")
code, d = req("/api/settings/schedule", "PUT", {"enabled": False, "cron": "0 0 20 * * *", "zone": "Asia/Shanghai"}, csrf)
print(f"   HTTP {code} → {d.get('message')} | enabled={d.get('enabled')} nextRun={d.get('nextRun') or '(无)'}")

print("⑧ 恢复每天 10:00")
code, d = req("/api/settings/schedule", "PUT", {"enabled": True, "cron": "0 0 10 * * *", "zone": "Asia/Shanghai"}, csrf)
print(f"   HTTP {code} → {d.get('message')} | {d.get('describe')} | 下次 {d.get('nextRun')}")

print("⑨ 5 段写法自动补秒")
code, d = req("/api/settings/schedule", "PUT", {"enabled": True, "cron": "30 8 * * *", "zone": "Asia/Shanghai"}, csrf)
print(f"   HTTP {code} → 归一化 {d.get('cron')} | {d.get('describe')}")

print("⑩ 每周一 08:00")
code, d = req("/api/settings/schedule", "PUT", {"enabled": True, "cron": "0 0 8 * * 1", "zone": "Asia/Shanghai"}, csrf)
print(f"   HTTP {code} → {d.get('describe')} | 下次 {d.get('nextRun')}")

print("⑪ 每 6 小时")
code, d = req("/api/settings/schedule", "PUT", {"enabled": True, "cron": "0 0 */6 * * *", "zone": "Asia/Shanghai"}, csrf)
print(f"   HTTP {code} → {d.get('describe')} | 下次 {d.get('nextRun')}")

print("⑫ 恢复原配置")
code, d = req("/api/settings/schedule", "PUT", {"enabled": True, "cron": "0 0 10 * * *", "zone": "Asia/Shanghai"}, csrf)
print(f"   HTTP {code} → {d.get('message')} | {d.get('describe')}")
