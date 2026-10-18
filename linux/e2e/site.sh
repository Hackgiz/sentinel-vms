#!/bin/bash
# site.sh — bring a local Sentinel test site up or down.
#   site.sh up      start cameras, management server, enroll 2 recording servers, add 8 cameras
#   site.sh down    stop what this script started (pids in run/ only)
#   site.sh status  what's running
#   site.sh ports   print the port plan
#   site.sh clean   delete the site's state (refuses while anything runs)
#   site.sh start-one|stop-one NAME   one service (used by checks.py)
# Everything lives under this directory; nothing touches a real install.
#
# Ports: every port is offset from the historical layout (MS on 18090) by
# E2E_PORT_BASE - 18090. E2E_PORT_BASE defaults to 28090, so a live site on
# 18090/18091 and its MediaMTX ports is never touched. `up` refuses to start
# anything if a port it needs is already in use by someone else.
set -euo pipefail
T="$(cd "$(dirname "$0")" && pwd)"
BIN="$T/bin"; RUN="$T/run"; LOG="$T/log"
BASE=${E2E_PORT_BASE:-28090}
O=$((BASE - 18090))
if [ "$BASE" -le 18999 ] && [ "$BASE" -ge 17000 ]; then
    echo "refusing E2E_PORT_BASE=$BASE: overlaps the live site's port range (17000-18999)" >&2; exit 2
fi
if [ $((BASE + 14500)) -gt 65535 ] || [ "$BASE" -lt 1024 ]; then
    echo "E2E_PORT_BASE=$BASE out of range (1024..51035)" >&2; exit 2
fi
MS_PORT=$((18090 + O)); CTL_PORT=$((18091 + O))
CAM_PORT=$((20554 + O))
MS="http://127.0.0.1:$MS_PORT"
ADMIN_USER=admin ADMIN_PASS='test-site-123'
rs_listen() { echo $((18090 + 100 * $1 + O)); }
rs_media()  { echo "$(($1*1000+20554+O)),$(($1*1000+20888+O)),$(($1*1000+20997+O)),$(($1*1000+20998+O))"; }
rs_webrtc() { echo $(($1*1000+20189+O)); }
mkdir -p "$RUN" "$LOG"

# ours PID: true when PID is alive AND its command line runs a binary from
# this directory's bin/ — a stale pidfile whose pid was reused by someone
# else's process is never signalled.
ours() {
    local pid=$1 cmd
    [ -n "$pid" ] || return 1
    cmd=$(ps -o command= -p "$pid" 2>/dev/null) || return 1
    case "$cmd" in "$BIN/"*) return 0;; *) return 1;; esac
}
running() { [ -f "$RUN/$1.pid" ] && ours "$(cat "$RUN/$1.pid")"; }

# port_free PORT: nobody listens on it (TCP or UDP), checked with lsof and a
# bind test on loopback and the wildcard address.
port_free() {
    local p=$1
    if lsof -nP -iTCP:"$p" -sTCP:LISTEN -t >/dev/null 2>&1; then return 1; fi
    if lsof -nP -iUDP:"$p" -t >/dev/null 2>&1; then return 1; fi
    python3 - "$p" <<'PY' || return 1
import socket, sys
p = int(sys.argv[1])
for fam, typ, addr in ((socket.AF_INET, socket.SOCK_STREAM, "127.0.0.1"),
                       (socket.AF_INET, socket.SOCK_STREAM, "0.0.0.0"),
                       (socket.AF_INET, socket.SOCK_DGRAM, "0.0.0.0")):
    s = socket.socket(fam, typ)
    if typ == socket.SOCK_STREAM:
        # like Go's listeners: connections in TIME_WAIT don't block a bind,
        # a listener on the same address does (and lsof caught the rest)
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        s.bind((addr, p))
    except OSError:
        sys.exit(1)
    finally:
        s.close()
PY
}

# ports_for NAME: the ports service NAME binds.
ports_for() {
    case "$1" in
    cams) echo "$CAM_PORT";;
    ms)   echo "$MS_PORT $CTL_PORT";;
    rs1|rs2|rs3) local n=${1#rs}; echo "$(rs_listen "$n") $(rs_media "$n" | tr , ' ') $(rs_webrtc "$n")";;
    *) echo "";;
    esac
}

# preflight: every port needed by a service we are about to start must be free.
preflight() {
    local busy="" svc p
    for svc in cams ms rs1 rs2; do
        running "$svc" && continue
        for p in $(ports_for "$svc"); do
            port_free "$p" || busy="$busy $p($svc)"
        done
    done
    if [ -n "$busy" ]; then
        echo "ERROR: ports in use, refusing to start:$busy" >&2
        echo "       pick another range with E2E_PORT_BASE=<n> (default 28090)" >&2
        exit 1
    fi
}

start() { # name cmd...
    local name=$1; shift
    running "$name" && { echo "$name already running"; return; }
    "$@" >>"$LOG/$name.log" 2>&1 &
    echo $! >"$RUN/$name.pid"
    echo "started $name (pid $!)"
}

wait_http() { # url
    for _ in $(seq 1 60); do curl -fsS -o /dev/null "$1" 2>/dev/null && return 0; sleep 0.5; done
    echo "ERROR: $1 never came up" >&2; return 1
}

api() { # method path [json] — v2 call as admin; token cached in run/token
    local m=$1 p=$2 body=${3:-}
    curl -fsS -X "$m" -H "Authorization: Bearer $(cat "$RUN/token")" \
        ${body:+-H "Content-Type: application/json" -d "$body"} "$MS$p"
}

stop_one() { # pidfile
    local f=$1 pid n
    n=$(basename "$f" .pid); pid=$(cat "$f" 2>/dev/null || true)
    if ours "$pid"; then
        # children first (a recording server's MediaMTX), only of our own pid
        pkill -TERM -P "$pid" 2>/dev/null || true
        kill "$pid" 2>/dev/null || true
        for _ in $(seq 1 80); do kill -0 "$pid" 2>/dev/null || break; sleep 0.25; done
        echo "stopped $n"
    else
        echo "skipped $n (pid $pid not ours or gone)"
    fi
    rm -f "$f"
}

case "${1:-}" in
ports)
    for svc in cams ms rs1 rs2 rs3; do echo "$svc: $(ports_for "$svc")"; done
    ;;
up)
    preflight
    echo "port base $BASE (MS $MS_PORT, control $CTL_PORT, cameras $CAM_PORT)"
    # --- cameras: one MediaMTX accepting RTSP publishes, 8 ffmpeg test patterns ---
    cat >"$RUN/cams.yml" <<EOF
logLevel: warn
api: no
rtmp: no
hls: no
webrtc: no
srt: no
moq: no
playback: no
rtspAddress: 127.0.0.1:$CAM_PORT
rtspTransports: [tcp]
paths:
  all_others:
EOF
    start cams "$BIN/mediamtx" "$RUN/cams.yml"
    sleep 1
    for i in $(seq 1 8); do
        start cam$i "$BIN/ffmpeg" -hide_banner -loglevel error -re \
            -f lavfi -i "testsrc2=size=640x360:rate=10" \
            -c:v h264_videotoolbox -realtime 1 -b:v 400k -g 20 \
            -f rtsp -rtsp_transport tcp "rtsp://127.0.0.1:$CAM_PORT/cam$i"
    done

    # --- management server (no local recorder) ---
    start ms "$BIN/sentinel" -role management -data "$T/ms" -listen 127.0.0.1:$MS_PORT \
        -control-listen 127.0.0.1:$CTL_PORT -control-hosts 127.0.0.1 -webrtc-port 0
    wait_http "$MS/api/v1/status"

    # first admin + v2 token
    curl -fsS -X POST -H 'Content-Type: application/json' \
        -d "{\"name\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" "$MS/api/v1/setup" >/dev/null 2>&1 || true
    curl -fsS -X POST -H 'Content-Type: application/json' \
        -d "{\"grantType\":\"password\",\"name\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" "$MS/api/v2/auth/token" \
        | python3 -c 'import json,sys; print(json.load(sys.stdin)["accessToken"], end="")' >"$RUN/token"

    # --- two recording servers, each with its own data dir, API port and media ports ---
    for rs in 1 2; do
        if [ ! -d "$T/rs$rs/link" ]; then  # not yet enrolled
            TOK=$(api POST /api/v2/recording-servers/enrollment-tokens "{\"name\":\"RS $rs\"}" \
                | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"], end="")')
            ENROLL="-enroll $TOK"
        else
            ENROLL=""
        fi
        L=$(rs_listen $rs)
        SENTINEL_MEDIAMTX_PORTS="$(rs_media $rs)" \
        start rs$rs "$BIN/sentinel" -role recording -data "$T/rs$rs" -listen 127.0.0.1:$L \
            -management https://127.0.0.1:$CTL_PORT -video-url "http://127.0.0.1:$L" \
            -webrtc-port "$(rs_webrtc $rs)" $ENROLL
        wait_http "http://127.0.0.1:$L/api/v1/status"
    done
    sleep 2

    # --- 8 cameras: odd on RS 1, even on RS 2 ---
    SERVERS=$(api GET /api/v2/recording-servers)
    RS1=$(echo "$SERVERS" | python3 -c 'import json,sys; d=json.load(sys.stdin); print([s["id"] for s in d["items"] if s["name"]=="RS 1"][0], end="")')
    RS2=$(echo "$SERVERS" | python3 -c 'import json,sys; d=json.load(sys.stdin); print([s["id"] for s in d["items"] if s["name"]=="RS 2"][0], end="")')
    echo "$RS1" >"$RUN/rs1.id"; echo "$RS2" >"$RUN/rs2.id"
    EXISTING=$(api GET /api/v2/cameras | python3 -c 'import json,sys; print(" ".join(c["name"] for c in json.load(sys.stdin)["items"]))')
    for i in $(seq 1 8); do
        case " $EXISTING " in *" Cam $i "*) continue;; esac
        [ $((i % 2)) = 1 ] && R=$RS1 || R=$RS2
        api POST /api/v2/cameras "{\"name\":\"Cam $i\",\"rtspUrl\":\"rtsp://127.0.0.1:$CAM_PORT/cam$i\",\"recorderId\":\"$R\",\"retentionDays\":1}" >/dev/null
        echo "added Cam $i on $R"
    done
    echo; echo "Site is up: $MS  (sign in: $ADMIN_USER / $ADMIN_PASS)"
    ;;
start-one) # start-one ms|rs1|rs2|rs3 [enroll-token] — (re)start one service (checks.py uses it)
    svc=${2:-}
    case "$svc" in
    ms|rs1|rs2|rs3) ;;
    *) echo "usage: site.sh start-one ms|rs1|rs2|rs3 [enroll-token]" >&2; exit 2;;
    esac
    running "$svc" && { echo "$svc already running"; exit 0; }
    for p in $(ports_for "$svc"); do
        # a service we just stopped may take a moment to let go of its ports
        for _ in $(seq 1 20); do port_free "$p" && break; sleep 0.5; done
        port_free "$p" || { echo "ERROR: port $p in use, refusing to start $svc" >&2; exit 1; }
    done
    if [ "$svc" = ms ]; then
        start ms "$BIN/sentinel" -role management -data "$T/ms" -listen 127.0.0.1:$MS_PORT \
            -control-listen 127.0.0.1:$CTL_PORT -control-hosts 127.0.0.1 -webrtc-port 0
        wait_http "$MS/api/v1/status"
    else
        n=${svc#rs}; L=$(rs_listen $n)
        SENTINEL_MEDIAMTX_PORTS="$(rs_media $n)" \
        start "$svc" "$BIN/sentinel" -role recording -data "$T/$svc" -listen 127.0.0.1:$L \
            -management https://127.0.0.1:$CTL_PORT -video-url "http://127.0.0.1:$L" \
            -webrtc-port "$(rs_webrtc $n)" ${3:+-enroll $3}
        wait_http "http://127.0.0.1:$L/api/v1/status"
    fi
    ;;
stop-one) # stop-one NAME — stop one of our services by its pidfile
    [ -f "$RUN/${2:-}.pid" ] && stop_one "$RUN/$2.pid" || echo "${2:-?} not running"
    ;;
down)
    for f in "$RUN"/*.pid; do
        [ -f "$f" ] || continue
        stop_one "$f"
    done
    ;;
clean) # delete the site's state (data dirs, logs) — only while nothing of ours runs
    for f in "$RUN"/*.pid; do
        [ -f "$f" ] || continue
        ours "$(cat "$f")" && { echo "ERROR: $(basename "$f" .pid) is running; site.sh down first" >&2; exit 1; }
    done
    rm -rf "$T/ms" "$T/rs1" "$T/rs2" "$T/rs3" "$T/probe" "$RUN" "$LOG"
    echo "cleaned"
    ;;
status)
    for f in "$RUN"/*.pid; do
        [ -f "$f" ] || continue
        n=$(basename "$f" .pid)
        ours "$(cat "$f")" && echo "$n: running (pid $(cat "$f"))" || echo "$n: DEAD"
    done
    ;;
*) echo "usage: site.sh up|down|status|ports|clean|start-one|stop-one" >&2; exit 2;;
esac
