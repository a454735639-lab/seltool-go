import json,os,time,urllib.request as U
ROOT='/test';CFG=ROOT+'/config/custom.yml'
mode=os.environ.get('PHASE','prepare')
checks=[]
def add(n,b,d=''):checks.append({'name':n,'result':'PASS' if b else 'FAIL','detail':d});print(checks[-1],flush=True)
def get(path):
 with U.build_opener(U.ProxyHandler({})).open('http://acceptapp:8080'+path,timeout=8) as f:return json.load(f)
if mode=='prepare':
 s=open(CFG).read().replace('run-on-startup: false','run-on-startup: true').replace('cron: "0 0 1 1 * *"','cron: "*/10 * * * * *"')
 open(CFG,'w').write(s)
 # Keep target data unchanged to verify persistence.
 state=json.load(open(ROOT+'/results.json'))
 snap={k:open(ROOT+'/config/'+k).read() for k in ['user-account.json','targets.json','cookiecloud-settings.json']}
 snap['hit_before']=get('/api/auth/status').get('registered')
 # fixture events are non-sensitive fake task messages
 with U.build_opener(U.ProxyHandler({})).open('http://mock.test:8000/admin/state') as f:st=json.load(f)
 snap['events_before']=len(st['events']);snap['hits_before']=st['hits'].get('/ok',0)
 json.dump(snap,open(ROOT+'/restart-private.json','w'));os.chmod(ROOT+'/restart-private.json',0o600)
 print('RESTART_PREPARED',flush=True)
else:
 snap=json.load(open(ROOT+'/restart-private.json'))
 for k in ['user-account.json','targets.json','cookiecloud-settings.json']:
  add('重启持久化 '+k,open(ROOT+'/config/'+k).read()==snap[k])
 add('重启后仍已注册',get('/api/auth/status').get('registered')==True)
 with U.build_opener(U.ProxyHandler({})).open('http://mock.test:8000/admin/state') as f:st=json.load(f)
 after=st['events'][snap['events_before']:]
 add('重启启动任务真实执行',any('启动执行任务已结束' in x['text'] for x in after))
 add('六段Cron定时真实执行',any('定时执行任务已结束' in x['text'] for x in after))
 h=json.load(open(ROOT+'/config/target-run-history.json'));rec=h.get('http://mock.test:8000/ok',[{}])[0]
 add('重启后浏览器仍成功',rec.get('success')==True,rec.get('message',''))
 r=json.load(open(ROOT+'/results.json'));r['checks'].extend(checks)
 r['passed']=sum(x['result']=='PASS' for x in r['checks']);r['failed']=sum(x['result']=='FAIL' for x in r['checks'])
 json.dump(r,open(ROOT+'/results.json','w'),ensure_ascii=False,indent=2)
 os.remove(ROOT+'/restart-private.json')
