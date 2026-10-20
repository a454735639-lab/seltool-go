set -eu
cd /volume1/docker/seltool-f1-work
for i in 2 3; do
  docker restart sel-candidate-app >/dev/null
  for j in $(seq 1 10); do
    if docker exec sel-candidate-mock python -c "import urllib.request; urllib.request.urlopen('http://acceptapp:8080/api/auth/status')" >/dev/null 2>&1; then break; fi
  done
  docker exec sel-candidate-app /app/vncproxy -tcp-probe 127.0.0.1:5900
  echo "FINAL_RESTART_${i}_READY"
done
docker exec sel-candidate-app sh -c 'pgrep -c chromium || echo 0'
echo FINAL_RESTARTS_COMPLETE
