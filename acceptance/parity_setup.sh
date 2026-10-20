set -eu
R=/volume1/docker/seltool-acceptance-api
mkdir -p "$R/parity"
cat > "$R/parity/custom.yml" <<'EOF'
automation:
  run-on-startup: false
  schedule:
    cron: "0 0 1 1 * *"
  targets:
    - name: yaml-migration-fixture
      url: http://mock.test:8000/ok
      warmupPath: /warmup
      localStorage: []
EOF
IMG=$(docker inspect seltool-go --format '{{.Image}}')
docker run -d --name sel-accept-parity --network sel-accept-api-net --network-alias acceptparity -e HTTP_PROXY= -e HTTPS_PROXY= -e NO_PROXY='*' -e no_proxy='*' -e SE_VNC_PASSWORD=accept-parity -v "$R/parity:/app/config" "$IMG" >/dev/null
docker exec sel-accept-api-mock python /test/parity_test.py
