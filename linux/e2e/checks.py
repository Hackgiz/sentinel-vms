#!/usr/bin/env python3
"""checks.py — acceptance checks against the local test site (site.sh up).

Usage: checks.py all | basic | outage | move | scope | audit
Prints PASS/FAIL per check and exits non-zero on any FAIL.
"""
import json, os, subprocess, sys, time, urllib.request, urllib.error

T = os.path.dirname(os.path.abspath(__file__))
MS = "http://127.0.0.1:18090"
ADMIN = ("admin", "test-site-123")
results = []


def req(method, path, body=None, token=None, base=MS, raw=False, headers=None):
    r = urllib.request.Request(base + path, method=method)
    if token:
        r.add_header("Authorization", "Bearer " + token)
    for k, v in (headers or {}).items():
        r.add_header(k, v)
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        r.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(r, data, timeout=15) as resp:
            b = resp.read()
            return resp.status, b if raw else (json.loads(b) if b else None)
    except urllib.error.HTTPError as e:
        b = e.read()
        try:
            return e.code, json.loads(b)
        except Exception:
            return e.code, b[:200]
    except Exception as e:
        return 0, str(e)


def signin(name, password):
    st, b = req("POST", "/api/v2/auth/token",
                {"grantType": "password", "name": name, "password": password})
    assert st == 200, (st, b)
    return b["accessToken"]


def check(name, ok, detail=""):
    results.append((name, bool(ok)))
    print(("PASS  " if ok else "FAIL  ") + name + (f"  — {detail}" if detail and not ok else ""))


def wait_for(desc, fn, timeout=120, every=2):
    end = time.time() + timeout
    while time.time() < end:
        if fn():
            return True
        time.sleep(every)
    print(f"  (timed out waiting for {desc})")
    return False


def servers(tok):
    st, b = req("GET", "/api/v2/recording-servers", token=tok)
    return {s["name"]: s for s in b["items"]} if st == 200 else {}


def cameras(tok):
    st, b = req("GET", "/api/v2/cameras", token=tok)
    return b["items"] if st == 200 else []


def online(c):
    st = c.get("status")
    return st.get("online") if isinstance(st, dict) else st == "Connected"


def video_token(tok, cam, rights=None, recorder=None):
    body = {}
    if rights:
        body["rights"] = rights
    if recorder:
        body["recorderId"] = recorder
    return req("POST", f"/api/v2/cameras/{cam}/video-token", body or None, token=tok)


def timeline(tok, cam, frm, to):
    return req("GET", f"/api/v2/cameras/{cam}/timeline?from={frm}&to={to}", token=tok)


# ---------------------------------------------------------------- basic ---
def t_basic(tok):
    sv = servers(tok)
    check("two recording servers enrolled", {"RS 1", "RS 2"} <= set(sv)
          and all(s.get("online") for n, s in sv.items() if n.startswith("RS")),
          json.dumps({n: s.get("online") for n, s in sv.items()}))
    check("local server hidden (management-only MS)", "This server" not in sv and len(sv) == 2,
          str(list(sv)))
    cams = cameras(tok)
    check("8 cameras, all Connected", len(cams) == 8 and all(online(c) for c in cams),
          str([(c["name"], c.get("status")) for c in cams if not online(c)]))

    # live video through the gateway for every camera
    bad = []
    for c in cams:
        st, vt = video_token(tok, c["id"])
        if st != 200:
            bad.append((c["name"], "token", st, vt)); continue
        st2, pl = req("GET", vt["gatewayUrl"] + f"/live/{c['id']}/index.m3u8", token=None, raw=True)
        if st2 != 200 or b"#EXTM3U" not in pl:
            bad.append((c["name"], "playlist", st2)); continue
    check("live HLS playlist for all 8 through the gateway", not bad, str(bad[:3]))

    # one segment with Content-Length (AVPlayer requirement)
    c0 = cams[0]
    st, vt = video_token(tok, c0["id"])
    st2, pl = req("GET", vt["gatewayUrl"] + f"/live/{c0['id']}/index.m3u8", raw=True)
    seg = next((l for l in pl.decode().splitlines() if l and not l.startswith("#")), None)
    segret = [None]
    def seg_cl():
        import http.client
        u = vt["gatewayUrl"] + f"/live/{c0['id']}/" + seg
        conn = http.client.HTTPConnection("127.0.0.1", 18090, timeout=15)
        conn.request("GET", u)
        r = conn.getresponse(); r.read()
        segret[0] = (r.status, r.getheader("Content-Length"))
        return r.status == 200 and r.getheader("Content-Length")
    ok = seg and wait_for("segment", seg_cl, 30)
    check("live segment has Content-Length", ok, f"seg={seg} got={segret[0]}")

    # recordings appear on both servers
    now = int(time.time() * 1000)
    def has_spans(cam):
        st, b = timeline(tok, cam["id"], now - 10 * 60_000, now + 60_000)
        return st == 200 and b.get("spans")
    ok1 = wait_for("spans cam1", lambda: has_spans(cams[0]), 180)
    ok2 = wait_for("spans cam2", lambda: has_spans(cams[1]), 180)
    check("recordings on RS 1 and RS 2 (timeline spans)", ok1 and ok2)

    # playback playlist via a playback token
    st, b = timeline(tok, cams[0]["id"], now - 10 * 60_000, now + 60_000)
    sp = b["spans"][0]
    check("timeline span carries recorderId", bool(sp.get("recorderId")), str(sp)[:120])
    st, vt = video_token(tok, cams[0]["id"], rights=["playback"])
    frm, to = sp["start"], sp.get("end") or now
    st2, pl = req("GET", vt["gatewayUrl"] + f"/playback/{cams[0]['id']}/index.m3u8?from={frm}&to={to}", raw=True)
    if st2 == 404:  # v1-style path still?
        st2, pl = req("GET", vt["gatewayUrl"] + f"/api/v1/cameras/{cams[0]['id']}/playback.m3u8?from={frm}&to={to}", raw=True)
    check("playback playlist via playback token", st2 == 200 and b"#EXTM3U" in (pl if isinstance(pl, bytes) else b""),
          f"{st2} {str(pl)[:100]}")


# --------------------------------------------------------------- outage ---
def t_outage(tok):
    pid = int(open(f"{T}/run/ms.pid").read().strip())
    cams = cameras(tok)
    cam1 = next(c for c in cams if c["name"] == "Cam 1")
    t0 = int(time.time() * 1000)
    subprocess.run(["kill", str(pid)], check=True)
    print("  management server stopped for 90 s; recording servers stay up …")
    time.sleep(90)
    # recording servers still alive?
    alive = all(subprocess.run(["kill", "-0", open(f"{T}/run/rs{i}.pid").read().strip()],
                               capture_output=True).returncode == 0 for i in (1, 2))
    check("recording servers alive through the outage", alive)
    # restart MS
    subprocess.run([f"{T}/site.sh", "up"], capture_output=True)
    ok = wait_for("MS back", lambda: req("GET", "/api/v1/status")[0] == 200, 60)
    tok2 = signin(*ADMIN)
    ok2 = wait_for("both RS reconnected",
                   lambda: all(s.get("online") for n, s in servers(tok2).items()), 120)
    check("management server back, both servers reconnected", ok and ok2)
    t1 = int(time.time() * 1000)

    # no recording gap across the outage
    def covered():
        st, b = timeline(tok2, cam1["id"], t0 - 60_000, t1)
        if st != 200 or not b.get("spans"):
            return False
        spans = sorted(b["spans"], key=lambda s: s["start"])
        # merge and look for a hole bigger than 15 s inside (t0, t1)
        end = spans[0]["start"]
        for s in spans:
            if s["start"] > end + 15_000 and end > t0:
                return False
            end = max(end, s.get("end") or t1)
        return end >= t1 - 30_000
    check("no recording gap during the outage", wait_for("coverage", covered, 60))

    # events replayed, no duplicates
    st, b = req("GET", f"/api/v2/events?from={t0-600_000}&to={t1}&limit=500", token=tok2)
    if st == 200:
        ids = [e["id"] for e in b.get("items", [])]
        check("events have no duplicate ids after replay", len(ids) == len(set(ids)), f"{len(ids)} events")
    else:
        check("events list after outage", False, str((st, b))[:120])
    return tok2


# ----------------------------------------------------------------- move ---
def t_move(tok):
    cams = cameras(tok)
    cam1 = next(c for c in cams if c["name"] == "Cam 1")
    sv = servers(tok)
    old = cam1["recorderId"]
    new = next(s["id"] for s in sv.values() if s["id"] != old)
    now = int(time.time() * 1000)
    st, b = timeline(tok, cam1["id"], now - 15 * 60_000, now)
    before = b.get("spans") if st == 200 else None
    st, b = req("PATCH", f"/api/v2/cameras/{cam1['id']}", {"recorderId": new}, token=tok)
    check("camera move accepted", st == 200, str((st, b))[:120])

    def recording_on_new():
        st, b = timeline(tok, cam1["id"], int(time.time()*1000) - 60_000, int(time.time()*1000))
        return st == 200 and any(s.get("recorderId") == new for s in b.get("spans", []))
    check("camera records on its new server", wait_for("new spans", recording_on_new, 180))

    # old footage still playable from the old server
    st, b = timeline(tok, cam1["id"], now - 15 * 60_000, now)
    oldspan = next((s for s in b.get("spans", []) if s.get("recorderId") == old), None)
    check("old footage still in the timeline (old recorderId)", bool(before) and bool(oldspan))
    if oldspan:
        st, vt = video_token(tok, cam1["id"], rights=["playback"], recorder=old)
        check("playback token for the old placement", st == 200, str((st, vt))[:120])
        if st == 200:
            frm, to = oldspan["start"], oldspan.get("end") or now
            st2, pl = req("GET", vt["gatewayUrl"] + f"/playback/{cam1['id']}/index.m3u8?from={frm}&to={to}", raw=True)
            if st2 == 404:
                st2, pl = req("GET", vt["gatewayUrl"] + f"/api/v1/cameras/{cam1['id']}/playback.m3u8?from={frm}&to={to}", raw=True)
            check("old footage plays after the move", st2 == 200, str(st2))
    # live token for the old placement must be refused
    st, b = video_token(tok, cam1["id"], rights=["live"], recorder=old)
    check("live for an old placement refused", st == 400, str((st, b))[:120])


# ---------------------------------------------------------------- scope ---
def t_scope(tok):
    cams = cameras(tok)
    by = {c["name"]: c for c in cams}
    st, g = req("POST", "/api/v2/camera-groups",
                {"name": "Lobby", "cameraIds": [by["Cam 2"]["id"], by["Cam 4"]["id"]]}, token=tok)
    if st == 409 or (st == 400 and "exists" in str(g)):
        st2, gl = req("GET", "/api/v2/camera-groups", token=tok)
        g = next(x for x in gl["items"] if x["name"] == "Lobby"); st = 201
    check("camera group created", st in (200, 201), str((st, g))[:120])
    st, u = req("POST", "/api/v2/users",
                {"name": "op", "password": "op-pass-123", "role": "Viewer",
                 "scope": {"allCameras": False, "groupIds": [g["id"]]}}, token=tok)
    if st in (400, 409) and "exists" in str(u):
        st = 201
    check("scoped operator created", st in (200, 201), str((st, u))[:120])

    op = signin("op", "op-pass-123")
    vis = cameras(op)
    check("operator sees only the 2 scoped cameras",
          sorted(c["name"] for c in vis) == ["Cam 2", "Cam 4"], str([c["name"] for c in vis]))

    hidden = by["Cam 3"]["id"]
    st, _ = req("GET", f"/api/v2/cameras/{hidden}", token=op)
    check("hidden camera GET → 404", st == 404, str(st))
    st, b = video_token(op, hidden)
    check("hidden camera video token refused", st in (403, 404), str((st, b))[:120])
    now = int(time.time() * 1000)
    st, _ = timeline(op, hidden, now - 600_000, now)
    check("hidden camera timeline refused", st in (403, 404), str(st))
    st, b = req("GET", f"/api/v2/cameras/{hidden}/snapshot", token=op, raw=True)
    check("hidden camera snapshot refused", st in (403, 404), str(st))
    # v1 dashboard API (cookie session) filters too; bearer tokens are refused
    st, _b = req("GET", "/api/v1/cameras", token=op)
    check("v1 refuses a bearer token", st == 401, str(st))
    import http.cookiejar
    jar = http.cookiejar.CookieJar()
    op_open = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    r = urllib.request.Request(MS + "/api/v1/login", method="POST",
                               data=json.dumps({"name": "op", "password": "op-pass-123"}).encode(),
                               headers={"Content-Type": "application/json"})
    op_open.open(r, timeout=15).read()
    b = json.loads(op_open.open(MS + "/api/v1/cameras", timeout=15).read())
    names = sorted(c["name"] for c in (b if isinstance(b, list) else b.get("cameras", [])))
    check("v1 camera list filtered", names == ["Cam 2", "Cam 4"], str(names))
    # an admin's live URL for a hidden camera: with the token being for the
    # admin, the URL itself still plays (tokens are bearer); what matters is
    # the operator can't MINT one. Check an operator-minted token for a
    # visible camera can't fetch the hidden camera's stream.
    st, vt = video_token(op, by["Cam 2"]["id"])
    if st == 200:
        st2, _ = req("GET", vt["gatewayUrl"] + f"/live/{hidden}/index.m3u8", raw=True)
        check("operator token doesn't open another camera's stream", st2 in (401, 403), str(st2))
    # recording servers list for the operator (open question: currently full)
    st, b = req("GET", "/api/v2/recording-servers", token=op)
    print(f"  note: operator GET /recording-servers → {st} ({'n/a' if st != 200 else str([s['name'] for s in b['items']])})")
    # saved view with a hidden camera must be refused
    st, b = req("POST", "/api/v2/views",
                {"name": "bad", "columns": 2, "rows": 2,
                 "tiles": [{"cameraId": hidden, "x": 0, "y": 0}]}, token=op)
    check("saved view with a hidden camera refused", st == 400, str((st, b))[:160])


# ---------------------------------------------------------------- audit ---
def t_audit(tok):
    st, b = req("GET", "/api/v2/audit/verify", token=tok)
    ok = (st == 200 and isinstance(b, dict) and b.get("ok")
          and all(sv.get("ok") for sv in b.get("servers", [])) and len(b.get("servers", [])) >= 2)
    check("audit verify (site + forwarded chains)", ok, str((st, b))[:200])
    sv = servers(tok)
    bad = []
    for n, s in sv.items():
        st, b = req("GET", f"/api/v2/recording-servers/{s['id']}/log?lines=50", token=tok, raw=True)
        if st != 200 or not b:
            bad.append((n, st))
    check("recent log fetched from both recording servers", not bad, str(bad))


if __name__ == "__main__":
    which = sys.argv[1] if len(sys.argv) > 1 else "all"
    tok = signin(*ADMIN)
    if which in ("all", "basic"):
        t_basic(tok)
    if which in ("all", "outage"):
        tok = t_outage(tok)
    if which in ("all", "move"):
        t_move(tok)
    if which in ("all", "scope"):
        t_scope(tok)
    if which in ("all", "audit"):
        t_audit(tok)
    fails = [n for n, ok in results if not ok]
    print(f"\n{len(results) - len(fails)}/{len(results)} checks passed" + (f"; FAILED: {fails}" if fails else ""))
    sys.exit(1 if fails else 0)
