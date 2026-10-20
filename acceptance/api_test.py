import json, urllib.request as U, urllib.error as E, http.cookiejar, time, secrets
B='http://acceptapp:8080';M='http://mock.test:8000'
jar=http.cookiejar.CookieJar();op=U.build_opener(U.ProxyHandler({}),U.HTTPCookieProcessor(jar))
plain=U.build_opener(U.ProxyHandler({}))
checks=[];csrf='';password=secrets.token_urlsafe(18)
def req(path,method='GET',body=None,auth=True,token=True,base=B):
 h={}
 if body is not None:h['Content-Type']='application/json';body=json.dumps(body).encode()
 if method not in ['GET','HEAD'] and token and csrf:h['X-CSRF-TOKEN']=csrf
 r=U.Request(base+path,data=body,headers=h,method=method)
 try:
  with (op if auth else plain).open(r,timeout=40) as f:code=f.status;raw=f.read()
 except E.HTTPError as e:code=e.code;raw=e.read()
 try: value=json.loads(raw)
 except:value=raw.decode(errors='replace')
 return code,value
def ck(name,truth,detail=''):
 checks.append({'name':name,'result':'PASS' if truth else 'FAIL','detail':detail})
 print(('PASS ' if truth else 'FAIL ')+name+(' | '+detail if detail else ''),flush=True)
def status():
 global csrf
 c,d=req('/api/auth/status');csrf=d.get('csrfToken','');return d
def waitdone(limit=50):
 end=time.time()+limit
 while time.time()<end:
  c,d=req('/api/automation/status')
  if c==200 and not d.get('running'):return True
  time.sleep(.25)
 return False
def target(path='/ok'):
 return {'name':'fixture '+path,'url':M+path,'warmupPath':'/warmup','okSelector':'#auth-ok','localStorage':[{'key':'accept-token','value':'test-storage-value'}]}
def settargets(ts):return req('/api/automation/targets','PUT',ts)
try:
 for i in range(20):
  try:d=status();break
  except Exception:time.sleep(.2)
 ck('首次启动无用户',d.get('registered')==False)
 ck('未登录API拒绝',req('/api/automation/targets',auth=False)[0]==401)
 ck('匿名CSRF拒绝',req('/api/auth/register','POST',{'username':'acceptadmin','password':password},token=False)[0]==403)
 ck('注册管理员',req('/api/auth/register','POST',{'username':'acceptadmin','password':password})[0]==201)
 ck('重复注册拒绝',req('/api/auth/register','POST',{'username':'acceptadmin','password':password})[0]==409)
 def login(pw):
  h={'Content-Type':'application/x-www-form-urlencoded','X-CSRF-TOKEN':csrf}
  import urllib.parse
  r=U.Request(B+'/api/auth/login',data=urllib.parse.urlencode({'username':'acceptadmin','password':pw}).encode(),headers=h)
  try:
   with op.open(r,timeout=10) as f:return f.status
  except E.HTTPError as e:return e.code
 ck('错误密码拒绝',login('wrong-test-password')==401)
 ck('正确密码登录',login(password)==200)
 d=status();ck('登录态与用户名',d.get('authenticated') and d.get('username')=='acceptadmin')
 ck('写操作CSRF拒绝',req('/api/automation/targets','PUT',[],token=False)[0]==403)
 ck('首页和index.html',req('/')[0]==200 and req('/index.html')[0]==200)
 ck('静态认证资源',all(req(p)[0]==200 for p in ['/login.html','/register.html','/auth.css']))
 c,ts=req('/api/automation/target-templates');ck('内置4个模板',c==200 and len(ts)==4)
 ck('非法URL拒绝',settargets([{'url':'javascript:alert(1)'}])[0]==400)
 ck('目标保存读取',settargets([target()])[0]==200 and req('/api/automation/targets')[1][0]['url']==M+'/ok')
 c,d=req('/api/settings/cookiecloud');ck('凭据不回显密码',c==200 and d.get('hasPassword') and 'password' not in d)
 c,d=req('/api/settings/cookiecloud/test','POST',{});ck('CookieCloud测试计数',c==200 and d.get('cookies')==1 and d.get('domains')==1)
 c,d=req('/api/settings/cookiecloud','PUT',{'url':M+'/cookiecloud','key':'accept-key','password':''});ck('留空沿用密码',c==200 and d.get('hasPassword'))
 c,d=req('/api/automation/run','POST',{});ck('手动执行202',c==202)
 done=waitdone();c,latest=req('/api/automation/targets/history/latest');rec=latest.get(M+'/ok',{})
 ck('Cookie与localStorage均有效',done and rec.get('success')==True,rec.get('message','无记录'))
 st=req('/admin/state',base=M)[1];ck('HTTPOnly Cookie实际送达',st['last_request'].get('cookieOK')==True)
 c,d=req('/api/automation/targets/history?url='+U.quote(M+'/ok',safe=''));ck('目标历史可读取',c==200 and isinstance(d,list) and len(d)>0)
 settargets([target('/slow')]);c,_=req('/api/automation/run','POST',{});busy=req('/api/automation/run','POST',{})[0];ck('运行互斥409',c==202 and busy==409);waitdone()
 settargets([target('/fail'),target('/ok')]);req('/api/automation/run','POST',{});done=waitdone();latest=req('/api/automation/targets/history/latest')[1]
 ck('单站失败不中断下站',done and latest.get(M+'/fail',{}).get('success')==False and latest.get(M+'/ok',{}).get('success')==True)
 ck('不存在目标重试404',req('/api/automation/targets/retry?url='+U.quote(M+'/missing',safe=''),'POST',{})[0]==404)
 c,d=req('/api/automation/targets/retry-failed','POST',{});ck('失败批量重试仅选失败',c==202 and d.get('targetUrls')==[M+'/fail']);waitdone()
 c,_=req('/api/automation/targets/retry?url='+U.quote(M+'/ok',safe=''),'POST',{});ck('单目标重试',c==202 and waitdone())
 for i in range(7):
  req('/api/automation/targets/retry?url='+U.quote(M+'/ok',safe=''),'POST',{});waitdone()
 c,hist=req('/api/automation/targets/history?url='+U.quote(M+'/ok',safe=''));ck('历史保留7条',c==200 and len(hist)==7)
 c,d=req('/api/automation/targets/import','POST',{'mode':'manual','input':M+'/ok\nhttps://example.org/path\nbogus'});ck('手动导入保留路径去重',c==200 and d.get('imported')==1 and d.get('skipped')==1)
 c,d=req('/api/automation/targets/import','POST',{'mode':'manual','input':'https://example.org/path'});ck('重复导入不新增',c==200 and d.get('imported')==0)
 req('/admin/mode','POST',{'mode':'unavailable'},base=M)
 c,d=req('/api/settings/cookiecloud/test','POST',{});ck('测试连接不应掩盖远端503',c>=400,'HTTP '+str(c)+'; ok='+str(d.get('ok')))
 settargets([target('/ok')]);req('/api/automation/run','POST',{});done=waitdone();latest=req('/api/automation/targets/history/latest')[1]
 ck('远端失败时缓存回退',done and latest.get(M+'/ok',{}).get('success')==True)
 req('/admin/mode','POST',{'mode':'ok'},base=M)
 # Isolated settings: a wrong password must fail the diagnostic even with an existing cache.
 req('/api/settings/cookiecloud','PUT',{'url':M+'/cookiecloud','key':'accept-key','password':'wrong-test-password'})
 c,d=req('/api/settings/cookiecloud/test','POST',{});ck('测试连接应拒绝错误密码',c>=400,'HTTP '+str(c)+'; ok='+str(d.get('ok')))
 req('/api/settings/cookiecloud','PUT',{'url':M+'/cookiecloud','key':'accept-key','password':'accept-password'})
 st=req('/admin/state',base=M)[1];ck('Webhook收到任务汇总',any('目标数:' in x['text'] and '成功:' in x['text'] for x in st['events']))
 ck('登出成功',req('/api/auth/logout','POST',{})[0]==200)
 ck('登出后API拒绝',req('/api/automation/targets')[0]==401)
except Exception as e:
 ck('验收脚本完整执行',False,type(e).__name__+': '+str(e))
finally:
 with open('/test/results.json','w') as f:json.dump({'checks':checks,'passed':sum(x['result']=='PASS' for x in checks),'failed':sum(x['result']=='FAIL' for x in checks)},f,ensure_ascii=False,indent=2)
 print('RESULTS',sum(x['result']=='PASS' for x in checks),'PASS',sum(x['result']=='FAIL' for x in checks),'FAIL',flush=True)
