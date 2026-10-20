set -eu
ROOT=/volume1/docker/seltool-f1-work
cd "$ROOT"
mkdir -p ui-run/config
cp candidate-run/config/custom.yml ui-run/config/custom.yml
printf '[]' > ui-run/config/targets.json
docker run -d --name sel-candidate-ui --network seltool-candidate-accept -p 127.0.0.1:18096:8080 -e NO_PROXY='*' -e no_proxy='*' -e SE_VNC_PASSWORD=fixture1 -v "$ROOT/ui-run/config:/app/config" seltool-go:candidate-safe >/dev/null
echo ISOLATED_UI_STARTED
