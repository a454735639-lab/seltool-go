import json, time, threading
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
state={'mode':'ok','events':[],'hits':{},'last_request':{}}
class Handler(BaseHTTPRequestHandler):
 def log_message(self,*args): pass
 def send(self,code,body,kind='application/json'):
  if not isinstance(body,(str,bytes)): body=json.dumps(body)
  if isinstance(body,str): body=body.encode()
  self.send_response(code);self.send_header('Content-Type',kind);self.send_header('Content-Length',str(len(body)));self.end_headers()
  try:self.wfile.write(body)
  except (BrokenPipeError,ConnectionResetError):pass
 def do_GET(self):
  p=self.path.split('?')[0];state['hits'][p]=state['hits'].get(p,0)+1
  if p=='/admin/state':return self.send(200,state)
  if p=='/warmup':return self.send(200,'<html><title>warmup</title><body>init</body></html>','text/html')
  if p=='/slow':time.sleep(3)
  if p=='/hang':time.sleep(25)
  if p=='/fail':return self.send(200,'<html><title>Login</title><form action="takelogin.php"></form></html>','text/html')
  if p in ['/ok','/slow','/hang']:
   return self.send(200,'''<html><title>Acceptance target</title><body><p id="pending">checking</p><script>
fetch('/verify').then(r=>r.json()).then(d=>{if(d.cookieOK && localStorage.getItem('accept-token')==='test-storage-value'){let e=document.createElement('div');e.id='auth-ok';e.textContent='authenticated';document.body.appendChild(e)}})
</script></body></html>''','text/html')
  if p=='/verify':
   ok='accept_sid=test-cookie-value' in self.headers.get('Cookie','')
   state['last_request']={'cookieOK':ok,'path':p}
   return self.send(200,{'cookieOK':ok})
  if p=='/':return self.send(200,'<html><title>Acceptance</title><body>mock</body></html>','text/html')
  return self.send(404,{'error':'missing'})
 def do_POST(self):
  n=int(self.headers.get('Content-Length','0'));b=self.rfile.read(n)
  try: d=json.loads(b) if b else {}
  except: d={}
  if self.path=='/admin/mode':state['mode']=d.get('mode','ok');return self.send(200,{'ok':True})
  if self.path=='/notify':state['events'].append({'time':time.time(),'text':d.get('text',{}).get('content','')});return self.send(200,{'errcode':0})
  if self.path=='/cookiecloud/get/accept-key':
   if state['mode']=='unavailable':return self.send(503,{'error':'test_unavailable'})
   if state['mode']=='missing':return self.send(404,{'error':'test_missing'})
   if d.get('password')!='accept-password':return self.send(500,{'error':'test_decrypt_error'})
   return self.send(200,{'cookie_data':{'mock.test':[{'name':'accept_sid','value':'test-cookie-value','domain':'mock.test','path':'/','httpOnly':True,'secure':False}]},'local_storage_data':{}})
  return self.send(404,{'error':'missing'})
ThreadingHTTPServer(('0.0.0.0',8000),Handler).serve_forever()
