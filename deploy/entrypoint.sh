#!/bin/bash
set -euo pipefail

VNC_PORT="${VNC_PORT:-5900}"
NOVNC_PORT="${NOVNC_PORT:-7900}"
: "${SE_VNC_PASSWORD:=selenium}"

export DISPLAY=:99

log() { echo "[entrypoint] $*"; }
die() { echo "[entrypoint] 失败: $*" >&2; exit 1; }

# 轮询直到命令成功；参数：说明 超时秒 命令...
wait_for() {
  local what="$1" timeout="$2"; shift 2
  local deadline=$((SECONDS + timeout))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if "$@" >/dev/null 2>&1; then
      log "$what 就绪"
      return 0
    fi
    sleep 0.2
  done
  return 1
}

# 1. 虚拟显示
# 容器被强制重启时 /tmp 里的 X 锁会残留，Xvfb 会因此拒绝启动，必须先清掉
rm -f /tmp/.X99-lock /tmp/.X11-unix/X99
# 分辨率与浏览器窗口（--window-size=1400,900）匹配：1440x960 留出
# fluxbox 标题栏与边框空间，避免窗口被裁切。
# 原为 1920x1080，多出来的帧缓冲白占内存（实测省 10MB 左右）。
Xvfb :99 -screen 0 "${XVFB_SCREEN:-1440x960x24}" -nolisten tcp -ac >/tmp/xvfb.log 2>&1 &
XVFB_PID=$!
if ! wait_for "Xvfb :99" 20 test -S /tmp/.X11-unix/X99; then
  kill -0 "$XVFB_PID" 2>/dev/null || { cat /tmp/xvfb.log >&2; die "Xvfb 启动失败"; }
  die "Xvfb 20s 内未创建 X socket"
fi
kill -0 "$XVFB_PID" 2>/dev/null || { cat /tmp/xvfb.log >&2; die "Xvfb 已退出"; }

# 2. 窗口管理器：没有它 Chrome 窗口在 VNC 里不显示
fluxbox >/tmp/fluxbox.log 2>&1 &
FLUXBOX_PID=$!

# fluxbox 找不到壁纸工具（feh/xsetroot 等）时会弹一个 xmessage 错误框，
# 白占 3MB 且会出现在 VNC 画面里。这里等它出现后清掉。
(
  for _ in $(seq 1 20); do
    if pkill -f "xmessage.*fbsetbg" 2>/dev/null; then
      log "已清理 fluxbox 壁纸错误弹框"
      break
    fi
    sleep 0.5
  done
) &

# 3. VNC 服务（密码来自环境变量）
x11vnc -storepasswd "$SE_VNC_PASSWORD" /tmp/vncpass >/dev/null 2>&1
x11vnc -display :99 -forever -shared -rfbport "$VNC_PORT" \
       -rfbauth /tmp/vncpass -quiet -noxdamage >/tmp/x11vnc.log 2>&1 &
X11VNC_PID=$!

# 真实就绪判断：用 vncproxy 自己的 -tcp-probe 读 RFB 横幅，不依赖 python/node/curl
if ! wait_for "x11vnc :$VNC_PORT" 30 /app/vncproxy -tcp-probe "127.0.0.1:$VNC_PORT" -tcp-probe-timeout 1s; then
  kill -0 "$X11VNC_PID" 2>/dev/null || { cat /tmp/x11vnc.log >&2; die "x11vnc 已退出"; }
  cat /tmp/x11vnc.log >&2
  die "x11vnc 30s 内未提供 RFB 服务"
fi

# 4. noVNC：Go 写的 WebSocket 代理，替代 websockify（省掉 python3）
/app/vncproxy -listen ":$NOVNC_PORT" -target "127.0.0.1:$VNC_PORT" -web /usr/share/novnc >/tmp/vncproxy.log 2>&1 &
VNCPROXY_PID=$!

# 代理起来但后端还没好时 /websockify 会 502，这里确认静态页和端口真的在服务
if ! wait_for "vncproxy :$NOVNC_PORT" 15 \
     /bin/bash -c "exec 3<>/dev/tcp/127.0.0.1/$NOVNC_PORT"; then
  kill -0 "$VNCPROXY_PID" 2>/dev/null || { cat /tmp/vncproxy.log >&2; die "vncproxy 已退出"; }
  die "vncproxy 15s 内未监听 :$NOVNC_PORT"
fi

log "Xvfb :99 / VNC :$VNC_PORT / noVNC :$NOVNC_PORT 已启动"
exec /app/seltool -config /app/config/custom.yml
