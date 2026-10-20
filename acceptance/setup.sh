set -eu
cd /volume1/docker/seltool-go
ROOT=/volume1/docker/seltool-acceptance-api
mkdir -p "$ROOT/config" "$ROOT/data"
cat > "$ROOT/config/custom.yml" <<'EOF'
automation:
  listen: ":8080"
  run-on-startup: false
  schedule:
    cron: "0 0 1 1 * *"
    zone: "Asia/Shanghai"
  browser:
    headless: false
    no-sandbox: true
    disable-dev-shm-usage: true
    disable-images: true
    page-load-timeout: 6s
    page-stay-seconds: 0
    user-data-dir: "./data/chrome-profile"
  cookie-cloud:
    url: "http://mock.test:8000/cookiecloud"
    key: "accept-key"
    password: "accept-password"
    cache-file: "./config/cookiecloud-cache.json"
    allow-cache-fallback: true
  startup-notification:
    webhook-url: "http://mock.test:8000/notify"
EOF
IMAGE=$(docker inspect seltool-go --format '{{.Image}}')
echo "FROZEN_IMAGE=$IMAGE"
docker network create sel-accept-api-net >/dev/null
# The fixture network is internal to Docker; no published host ports.
docker run -d --name sel-accept-api-mock --network sel-accept-api-net --network-alias mock.test -e HTTP_PROXY= -e HTTPS_PROXY= -e http_proxy= -e https_proxy= -e NO_PROXY='*' -v "$ROOT:/test" python:3-alpine python /test/mock.py >/dev/null
docker run -d --name sel-accept-api --network sel-accept-api-net --network-alias acceptapp -e HTTP_PROXY= -e HTTPS_PROXY= -e http_proxy= -e https_proxy= -e NO_PROXY='*' -e no_proxy='*' -e SE_VNC_PASSWORD=accept-vnc -v "$ROOT/config:/app/config" -v "$ROOT/data:/app/data" "$IMAGE" >/dev/null
echo 'ISOLATED_ENV_STARTED'
