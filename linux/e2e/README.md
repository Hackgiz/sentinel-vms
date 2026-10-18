# e2e: a local multi-server test site

A whole Sentinel site on one machine, from scratch folders: one Management
Server, two enrolled Recording Servers and 8 fake cameras (ffmpeg test
patterns over RTSP), plus the acceptance checks for PLATFORM.md's phase 2
done criteria.

    ./run.sh            # clean, up, all checks, and always down
    ./run.sh scope      # the same for one phase

or by hand:

    ./site.sh up        # start everything (state lives next to the scripts)
    python3 checks.py   # all phases: basic direct outage move scope audit revoke
    ./site.sh down      # stop what site.sh started (and nothing else)
    ./site.sh clean     # delete the state (refuses while anything runs)

Phases: `basic` (enrolled servers, per-server snapshots, live + playback
through the gateway), `direct` (video tokens straight to a recording server,
verified there offline), `outage` (stops the Management Server for 90 s,
restarts one recording server meanwhile; expects no recording gap, events
queued in the outbox and replayed, no duplicates), `move` (camera moves,
old footage keeps playing), `scope` (a group-scoped operator against every
camera route in the spec, lists, the event stream and video URLs), `audit`
(site + forwarded chains, RS log fetch), `revoke` (enrolls a third server,
single-use tokens, CA pinning, renewal, revoke and what stops working).
Directory sign-in is covered by unit tests only.

`bin/` must hold `sentinel`, `fakeonvif` and `linkprobe` (from `linux/`:
`go build -o e2e/bin/ ./cmd/... ./e2e/linkprobe`), `mediamtx` (the version
scripts/build.sh pins, darwin build) and `ffmpeg`
(scripts/build-ffmpeg-macos.sh).

## Ports

Every port is the historical layout shifted by `E2E_PORT_BASE - 18090`;
`E2E_PORT_BASE` defaults to **28090**, so a live Sentinel on 18090/18091
and its MediaMTX ports is never touched. `./site.sh ports` prints the plan
(default: MS 28090, control link 28091, RS n on 28090+100n, cameras' RTSP
30554, RS n's MediaMTX 3n554/3n888/3n997/3n998 and WebRTC 3n189).
`site.sh up` refuses to start anything if a port it needs is in use, and a
base inside 17000–18999 is refused outright. `site.sh down` only signals
pids from its own `run/` pidfiles whose command line is a binary in this
`bin/`.

The site is plain HTTP on 127.0.0.1 with a fixed admin password: test use
only, never a template for a real install.
