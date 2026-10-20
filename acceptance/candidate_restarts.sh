set -eu
cd /volume1/docker/seltool-f1-work
python3 - <<'PY'
p='candidate-run/config/custom.yml';s=open(p).read().replace('run-on-startup: true','run-on-startup: false').replace('cron: "*/10 * * * * *"','cron: "0 0 1 1 * *"');open(p,'w').write(s)
PY
for i in 2 3; do
 docker restart sel-candidate-app >/dev/null
 for j in 1 2 3 4 5 6 7 8 9 10; do
  if docker exec sel-candidate-mock python -c "import urllib.request; urllib.request.urlopen('http://acceptapp:8080/api/auth/status')" >/dev/null 2>&1; then break; fi
 done
 docker exec sel-candidate-app /app/vncproxy -tcp-probe 127.0.0.1:5900
 echo "CANDIDATE_RESTART_${i}_READY"
done
docker exec sel-candidate-app sh -c 'pkill xmessage || true; xev -geometry 600x420+180+160 -name CANDIDATE_INPUT_TEST >/tmp/candidate-xev.log 2>&1 </dev/null &'
date -Is > candidate-run/steady-session-start
docker stats --no-stream --format '{{.Name}} {{.MemUsage}} {{.PIDs}}' sel-candidate-app > candidate-run/steady-start-resources.txt
mkdir -p candidate-run/parity-config
cp accept-run/parity-config/custom.yml candidate-run/parity-config/
docker run -d --name sel-candidate-parity --network seltool-candidate-accept --network-alias acceptparity -e NO_PROXY='*' -e no_proxy='*' -e SE_VNC_PASSWORD=fixture1 -v "$PWD/candidate-run/parity-config:/app/config" seltool-go:candidate-safe >/dev/null
docker exec sel-candidate-mock python /test/parity_test.py
docker rm -f sel-candidate-parity >/dev/null
echo CANDIDATE_RESTART_AND_PARITY_COMPLETE
