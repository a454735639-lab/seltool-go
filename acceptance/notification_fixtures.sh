set -eu
ROOT=/volume1/docker/seltool-f1-work
cd "$ROOT"
mkdir -p notify-badjson notify-browser
cat > notify-badjson/custom.yml <<'EOF'
automation:
  run-on-startup: false
  startup-notification:
    webhook-url: http://mock.test:8000/notify
EOF
printf 'invalid json fixture' > notify-badjson/targets.json
cat > notify-browser/custom.yml <<'EOF'
automation:
  run-on-startup: true
  schedule:
    cron: "0 0 1 1 * *"
  browser:
    binary-path: /missing-fixture-chromium
    user-data-dir: /tmp/notify-profile
  startup-notification:
    webhook-url: http://mock.test:8000/notify
EOF
printf '[{"url":"http://mock.test:8000/ok","localStorage":[]}]' > notify-browser/targets.json
# Both failure fixtures are synthetic and have no access to production volumes.
docker run --name sel-notify-badjson --network seltool-candidate-accept -e NO_PROXY='*' -e no_proxy='*' -e SE_VNC_PASSWORD=fixture1 -v "$ROOT/notify-badjson:/app/config" seltool-go:candidate-safe > notify-badjson.log 2>&1 && exit 1 || true
docker run -d --name sel-notify-browser --network seltool-candidate-accept -e NO_PROXY='*' -e no_proxy='*' -e SE_VNC_PASSWORD=fixture1 -v "$ROOT/notify-browser:/app/config" seltool-go:candidate-safe >/dev/null
echo NOTIFICATION_FAILURE_FIXTURES_STARTED
