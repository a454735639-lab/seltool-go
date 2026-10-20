import json, urllib.request as U, urllib.error as E, urllib.parse as P, http.cookiejar, secrets,time
base='http://acceptparity:8080';jar=http.cookiejar.CookieJar();op=U.build_opener(U.ProxyHandler({}),U.HTTPCookieProcessor(jar));checks=[];token=''
def req(p,m='GET',d=None):
 h={'X-CSRF-TOKEN':token}
 if d is not None:h['Content-Type']='application/json';d=json.dumps(d).encode()
 try:
  with op.open(U.Request(base+p,data=d,headers=h,method=m),timeout=10) as r:c=r.status;b=r.read()
 except E.HTTPError as e:c=e.code;b=e.read()
 try:return c,json.loads(b)
 except:return c,b.decode(errors='replace')
def ck(n,b,d=''):checks.append({'name':n,'result':'PASS' if b else 'FAIL','detail':d});print(checks[-1],flush=True)
for i in range(20):
 try:c,d=req('/api/auth/status');token=d['csrfToken'];break
 except:time.sleep(.2)
pw=secrets.token_urlsafe(16);req('/api/auth/register','POST',{'username':'acceptparity','password':pw})
r=U.Request(base+'/api/auth/login',data=P.urlencode({'username':'acceptparity','password':pw}).encode(),headers={'X-CSRF-TOKEN':token,'Content-Type':'application/x-www-form-urlencoded'})
op.open(r,timeout=10).close();token=req('/api/auth/status')[1]['csrfToken']
c,d=req('/api/automation/targets');ck('原版YAML目标配置生效',c==200 and len(d)==1,'实际目标数='+str(len(d)))
case={'templateId':'nonexistent-template','name':'invalid','url':'https://example.invalid/','warmupPath':'/favicon.ico','localStorage':[]}
c,d=req('/api/automation/targets','PUT',[case]);ck('拒绝不存在模板ID',c==400,'HTTP '+str(c))
case={'templateId':'mteam','name':'tampered','url':'https://example.invalid/changed','warmupPath':'/changed','localStorage':[]}
c,d=req('/api/automation/targets','PUT',[case]);ck('模板固定字段不可篡改',c==400,'HTTP '+str(c))
case={'templateId':'rousi','name':'rousi.pro','url':'https://rousi.pro/','warmupPath':'/favicon.ico','localStorage':[{'key':'token','value':''}]}
c,d=req('/api/automation/targets','PUT',[case]);ck('模板必填localStorage不可为空',c==400,'HTTP '+str(c))
req('/api/automation/targets','PUT',[])
json.dump(checks,open('/test/parity-results.json','w'),ensure_ascii=False,indent=2)
