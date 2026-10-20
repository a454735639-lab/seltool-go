set -eu
cd /volume1/docker/seltool-f1-work
python3 - <<'PY'
p='accept-run/config/custom.yml'
s=open(p).read().replace('run-on-startup: true','run-on-startup: false').replace('cron: "*/10 * * * * *"','cron: "0 0 1 1 * *"')
open(p,'w').write(s)
PY
for i in 2 3; do
 docker restart sel-f1-app >/dev/null
 for j in 1 2 3 4 5 6 7 8 9 10; do
  if docker exec sel-f1-mock python -c "import urllib.request; print(urllib.request.urlopen('http://acceptapp:8080/api/auth/status').status)" >/dev/null 2>&1; then break; fi
 done
 docker exec sel-f1-app /app/vncproxy -tcp-probe 127.0.0.1:5900
 echo "RESTART_${i}_READY"
done
docker exec sel-f1-app sh -c 'pkill xmessage || true; xev -geometry 600x420+180+160 -name F1_INPUT_TEST >/tmp/f1-xev.log 2>&1 </dev/null &'
echo RESTARTS_COMPLETE
