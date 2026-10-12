#!/bin/bash
# site.sh — bring a local Sentinel test site up or down.
#   site.sh up      start cameras, management server, enroll 2 recording servers, add 8 cameras
#   site.sh down    stop everything (pids in run/)
#   site.sh status  what's running
# Everything lives under this directory; nothing touches a real install.
set -euo pipefail
T="$(cd "$(dirname "$0")" && pwd)"
BIN="$T/bin"; RUN="$T/run"; LOG="$T/log"
MS="http://127.0.0.1:18090"
ADMIN_USER=admin ADMIN_PASS='test-site-123'
mkdir -p "$RUN" "$LOG"

start() { # name cmd...
    local name=$1; shift
    [ -f "$RUN/$name.pid" ] && kill -0 "$(cat "$RUN/$name.pid")" 2>/dev/null && { echo "$name already running"; return; }
    "$@" >"$LOG/$name.log" 2>&1 &
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

case "${1:-}" in
up)
    # --- cameras: one MediaMTX accepting RTSP publishes on :48554, 8 ffmpeg test patterns ---
    cat >"$RUN/cams.yml" <<EOF
logLevel: warn
api: no
rtmp: no
hls: no
webrtc: no
srt: no
rtspAddress: 127.0.0.1:20554
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
            -f rtsp -rtsp_transport tcp "rtsp://127.0.0.1:20554/cam$i"
    done

    # --- management server (no local recorder) ---
    start ms "$BIN/sentinel" -role management -data "$T/ms" -listen 127.0.0.1:18090 \
        -control-listen 127.0.0.1:18091 -control-hosts 127.0.0.1 -webrtc-port 0
    wait_http "$MS/api/v1/status"

    # first admin + v2 token
    curl -fsS -X POST -H 'Content-Type: application/json' \
        -d "{\"name\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" "$MS/api/v1/setup" >/dev/null || true
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
        SENTINEL_MEDIAMTX_PORTS="$((rs*1000+20554)),$((rs*1000+20888)),$((rs*1000+20997)),$((rs*1000+20998))" \
        start rs$rs "$BIN/sentinel" -role recording -data "$T/rs$rs" -listen 127.0.0.1:18$(($rs))90 \
            -management https://127.0.0.1:18091 -video-url "http://127.0.0.1:18$(($rs))90" \
            -webrtc-port $((rs*1000+20189)) $ENROLL
        wait_http "http://127.0.0.1:18$(($rs))90/api/v1/status"
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
        api POST /api/v2/cameras "{\"name\":\"Cam $i\",\"rtspUrl\":\"rtsp://127.0.0.1:20554/cam$i\",\"recorderId\":\"$R\",\"retentionDays\":1}" >/dev/null
        echo "added Cam $i on $R"
    done
    echo; echo "Site is up: $MS  (sign in: $ADMIN_USER / $ADMIN_PASS)"
    ;;
down)
    for f in "$RUN"/*.pid; do
        [ -f "$f" ] || continue
        pkill -P "$(cat "$f")" 2>/dev/null || true
        kill "$(cat "$f")" 2>/dev/null && echo "stopped $(basename "$f" .pid)" || true
        rm -f "$f"
    done
    ;;
status)
    for f in "$RUN"/*.pid; do
        [ -f "$f" ] || continue
        n=$(basename "$f" .pid)
        kill -0 "$(cat "$f")" 2>/dev/null && echo "$n: running (pid $(cat "$f"))" || echo "$n: DEAD"
    done
    ;;
*) echo "usage: site.sh up|down|status" >&2; exit 2;;
esac
