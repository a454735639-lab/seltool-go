set -eu
ROOT=/volume1/docker/seltool-f1-work
cd "$ROOT"
mkdir -p image/deploy accept-run/config accept-run/data
cp src/seltool src/vncproxy image/
cp src/deploy/entrypoint.sh image/deploy/
cp src/deploy/Dockerfile.f1 image/deploy/
chmod 755 image/deploy/entrypoint.sh image/seltool image/vncproxy
DOCKER_BUILDKIT=0 docker build --pull=false -f image/deploy/Dockerfile.f1 -t seltool-go:f1-pending image > f1-build.log 2>&1
cp src/acceptance/mock.py src/acceptance/api_test.py src/acceptance/restart_test.py src/acceptance/parity_test.py accept-run/
cat > accept-run/config/custom.yml <<'EOF'
automation:
  run-on-startup: false
  schedule:
    cron: "0 0 1 1 * *"
    zone: Asia/Shanghai
  browser:
    headless: false
    no-sandbox: true
    disable-dev-shm-usage: true
    page-load-timeout: 6s
    page-stay-seconds: 0
    user-data-dir: ./data/chrome-profile
  cookie-cloud:
    url: http://mock.test:8000/cookiecloud
    key: accept-key
    password: accept-password
    allow-cache-fallback: true
    cache-file: ./config/cookiecloud-cache.json
  startup-notification:
    webhook-url: http://mock.test:8000/notify
EOF
docker network create seltool-f1-accept >/dev/null
docker run -d --name sel-f1-mock --network seltool-f1-accept --network-alias mock.test -e HTTP_PROXY= -e HTTPS_PROXY= -e http_proxy= -e https_proxy= -e NO_PROXY='*' -v "$ROOT/accept-run:/test" python:3-alpine python /test/mock.py >/dev/null
docker run -d --name sel-f1-app --network seltool-f1-accept --network-alias acceptapp -p 127.0.0.1:18094:8080 -p 127.0.0.1:17904:7900 -e HTTP_PROXY= -e HTTPS_PROXY= -e http_proxy= -e https_proxy= -e NO_PROXY='*' -e no_proxy='*' -e SE_VNC_PASSWORD=fixture1 -e TZ=Asia/Shanghai -v "$ROOT/accept-run/config:/app/config" -v "$ROOT/accept-run/data:/app/data" -v "$ROOT/src:/test:ro" seltool-go:f1-pending >/dev/null
echo F1_ISOLATED_STARTED
