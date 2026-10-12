# e2e: a local multi-server test site

A whole Sentinel site on one machine, from scratch folders: one Management
Server, two enrolled Recording Servers and 8 fake cameras (ffmpeg test
patterns over RTSP), plus the acceptance checks for the phase 2–3 done
criteria.

    ./site.sh up        # start everything (state lives next to the scripts)
    python3 checks.py   # all checks: basic, outage, move, scope, audit
    ./site.sh down      # stop everything

`checks.py outage` stops the Management Server for 90 s and expects no
recording gap and no duplicated events. `checks.py <phase>` runs one phase.

`bin/` must hold `sentinel` and `fakeonvif` (go build ./cmd/...), `mediamtx`
(the version scripts/build.sh pins, darwin build) and `ffmpeg`
(scripts/build-ffmpeg-macos.sh). Ports used: 18090/18091 (MS), 181\*90 (RS),
2\*554/2\*888/2\*997/2\*998 (media), 20554 (cameras), 2\*189 (WebRTC).

The site is plain HTTP on 127.0.0.1 with a fixed admin password: test use
only, never a template for a real install.
