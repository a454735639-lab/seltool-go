import os,json,subprocess,datetime
C='seltool-go'
def get(path):
 p=subprocess.run(['docker','exec',C,'cat',path],capture_output=True,text=True,check=True)
 return json.loads(p.stdout)
t=get('/app/config/targets.json');h=get('/app/config/target-run-history.json')
active={x['url'] for x in t}
records=[h[u][0] for u in active if h.get(u)]
from urllib.parse import urlparse
failures=[{'host':urlparse(u).hostname,'message':h[u][0]['message'][:160]} for u in sorted(active) if h.get(u) and not h[u][0]['success']]
print(json.dumps({'failures':failures,'active_targets':len(t),'active_unique_urls':len(active),'recorded_targets':len(records),'success_latest':sum(bool(r['success']) for r in records),'failed_latest':sum(not r['success'] for r in records),'missing_history':len(active)-len(records),'oldest_latest_time':min([r['executedAt'] for r in records],default=''),'newest_latest_time':max([r['executedAt'] for r in records],default=''),'note':'生产最新历史的只读快照，不代表本轮隔离测试结果'},ensure_ascii=False,indent=2))
