set -eu
ROOT=/volume1/docker/seltool-f1-work
cd "$ROOT"
mkdir -p candidate-run/config candidate-run/data
cp src/acceptance/mock.py src/acceptance/api_test.py src/acceptance/restart_test.py src/acceptance/parity_test.py candidate-run/
# The saved initial fixture has no secrets or user targets.
cp accept-run/config/custom.yml candidate-run/config/custom.yml
rm -f candidate-run/config/targets.json
# No startup work or cron traffic during the long-session test.
python3 - <<'PY'
p='candidate-run/config/custom.yml';s=open(p).read().replace('run-on-startup: true','run-on-startup: false').replace('cron: "*/10 * * * * *"','cron: "0 0 1 1 * *"');open(p,'w').write(s)
PY
docker network create seltool-candidate-accept >/dev/null
docker run -d --name sel-candidate-mock --network seltool-candidate-accept --network-alias mock.test -e HTTP_PROXY= -e HTTPS_PROXY= -e http_proxy= -e https_proxy= -e NO_PROXY='*' -v "$ROOT/candidate-run:/test" python:3-alpine python /test/mock.py >/dev/null
docker run -d --name sel-candidate-app --network seltool-candidate-accept --network-alias acceptapp -p 127.0.0.1:18095:8080 -p 127.0.0.1:17905:7900 -e HTTP_PROXY= -e HTTPS_PROXY= -e http_proxy= -e https_proxy= -e NO_PROXY='*' -e no_proxy='*' -e SE_VNC_PASSWORD=fixture1 -e TZ=Asia/Shanghai -v "$ROOT/candidate-run/config:/app/config" -v "$ROOT/candidate-run/data:/app/data" -v "$ROOT/src:/test:ro" seltool-go:candidate-safe >/dev/null
date -Is > candidate-run/soak-start
docker stats --no-stream --format '{{.Name}} {{.MemUsage}} {{.PIDs}}' sel-candidate-app > candidate-run/start-resources.txt
(docker exec sel-candidate-mock python /test/api_test.py > candidate-run/api-run.log 2>&1; echo $? > candidate-run/api-run.rc) >/dev/null 2>&1 &
echo CANDIDATE_ACCEPTANCE_STARTED
