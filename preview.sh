#!/bin/sh
# 把改动推到 NAS 构建工作区并跑通知预览测试
set -e
cd /var/minis/workspace/seltool-f1
tar czf - "$@" | sshpass -p "$NAS_SSH_PASSWORD" ssh -o ConnectTimeout=15 -o StrictHostKeyChecking=no \
  "$NAS_SSH_USER@10.0.0.1" \
  'tar xzf - -C /volume1/docker/seltool-f1-work/src && cd /volume1/docker/seltool-f1-work && docker run --rm -e GOFLAGS=-mod=mod -e CGO_ENABLED=0 -v "$PWD/src:/src" -w /src golang:1.26-alpine go test -run TestPreviewNotifyPayloads -v 2>&1'
