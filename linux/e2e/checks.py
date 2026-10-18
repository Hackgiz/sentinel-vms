#!/usr/bin/env python3
"""checks.py — acceptance checks against the local test site (site.sh up).

Usage: checks.py all | basic | direct | outage | move | scope | audit | revoke
Prints PASS/FAIL per check and exits non-zero on any FAIL.

PLATFORM.md phase 2 → phase:
  1 control link (mTLS, enrollment, revoke, reconnect, outbox replay)
        basic, outage, revoke
  2 config snapshots per RS, assignment, moving a camera      basic, outage, move
  3 video tokens verified by the RS, direct to the RS, gateway direct, basic, move
  4 camera groups and scoped permissions                      scope
  5 central audit with forwarded RS entries, RS log fetch     audit, revoke
  6 directory sign-in: unit-tested (internal/directory, v2_directory_test.go);
    no directory server in this harness
"""
import base64, json, os, re, shutil, socket, subprocess, sys, time, urllib.request, urllib.error

T = os.path.dirname(os.path.abspath(__file__))
# Same port plan as site.sh: everything offset from the historical layout
# (MS on 18090) by E2E_PORT_BASE - 18090; default base 28090 keeps clear of a
# live site on 18090/18091.
BASE = int(os.environ.get("E2E_PORT_BASE", "28090"))
MS_PORT = BASE
CTL_PORT = BASE + 1
MS = f"http://127.0.0.1:{MS_PORT}"
ADMIN = ("admin", "test-site-123")
results = []


def rs_url(n):
    return f"http://127.0.0.1:{BASE + 100 * n}"


def req(method, path, body=None, token=None, base=MS, raw=False, headers=None, timeout=15):
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
        with urllib.request.urlopen(r, data, timeout=timeout) as resp:
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


def site(*args):
    """Run site.sh (same E2E_PORT_BASE); returns its output."""
    r = subprocess.run([f"{T}/site.sh", *args], capture_output=True, text=True)
    return r.stdout + r.stderr


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


def now_ms():
    return int(time.time() * 1000)


def servers(tok):
    st, b = req("GET", "/api/v2/recording-servers", token=tok)
    return {s["name"]: s for s in b["items"]} if st == 200 else {}


def cameras(tok):
    st, b = req("GET", "/api/v2/cameras", token=tok)
    return b["items"] if st == 200 else []


def cam_by_name(tok):
    return {c["name"]: c for c in cameras(tok)}


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


def rs_status(n):
    st, b = req("GET", "/api/v1/status", base=rs_url(n), timeout=5)
    return b if st == 200 and isinstance(b, dict) else {}


def covered(tok, cam, frm, to, hole=15_000):
    """The camera's timeline covers (frm, to) with no hole longer than `hole`."""
    st, b = timeline(tok, cam, frm - 60_000, to)
    if st != 200 or not b.get("spans"):
        return False
    spans = sorted(b["spans"], key=lambda s: s["start"])
    end = None
    for s in spans:
        if end is not None and s["start"] > end + hole and s["start"] > frm and end < to:
            return False
        e = s.get("end") or to
        end = e if end is None else max(end, e)
    return spans[0]["start"] <= frm + hole and end >= to - 30_000


def events(tok, frm, to, limit=500):
    st, b = req("GET", f"/api/v2/events?from={frm}&to={to}&limit={limit}", token=tok)
    return b.get("items", []) if st == 200 and isinstance(b, dict) else None


# ---------------------------------------------------------------- basic ---
def t_basic(tok):
    sv = servers(tok)
    check("two recording servers enrolled", {"RS 1", "RS 2"} <= set(sv)
          and all(s.get("online") for n, s in sv.items() if n.startswith("RS")),
          json.dumps({n: s.get("online") for n, s in sv.items()}))
    check("local server hidden (management-only MS)", "This server" not in sv and len(sv) == 2,
          str(list(sv)))
    # Snapshots per server: each RS runs only its own 4 cameras.
    counts = {n: s.get("cameras") for n, s in sv.items()}
    check("config snapshot per server (4 cameras each)", counts == {"RS 1": 4, "RS 2": 4}, str(counts))
    applied = wait_for("both servers applied their snapshot",
                       lambda: rs_status(1).get("cameras") == 4 and rs_status(2).get("cameras") == 4, 60)
    check("each recording server applied only its own cameras", applied,
          str((rs_status(1).get("cameras"), rs_status(2).get("cameras"))))

    def all_connected():
        cs = cameras(tok)
        return len(cs) == 8 and all(online(c) for c in cs)
    ok = wait_for("8 cameras connected", all_connected, 120)
    cams = cameras(tok)
    check("8 cameras, all Connected", ok,
          str([(c["name"], c.get("status")) for c in cams if not online(c)]))

    # live video through the gateway for every camera
    bad = []
    for c in cams:
        last = [None]

        def playlist():
            st, vt = video_token(tok, c["id"])
            if st != 200:
                last[0] = ("token", st, vt)
                return False
            st2, pl = req("GET", vt["gatewayUrl"] + f"/live/{c['id']}/index.m3u8", raw=True)
            last[0] = ("playlist", st2)
            return st2 == 200 and b"#EXTM3U" in pl
        if not wait_for(f"{c['name']} playlist", playlist, 45):
            bad.append((c["name"],) + last[0])
    check("live HLS playlist for all 8 through the gateway", not bad, str(bad[:3]))

    # one segment with Content-Length (AVPlayer requirement)
    c0 = cams[0]
    st, vt = video_token(tok, c0["id"])
    st2, pl = req("GET", vt["gatewayUrl"] + f"/live/{c0['id']}/index.m3u8", raw=True)
    seg = next((l for l in pl.decode(errors="replace").splitlines() if l and not l.startswith("#")), None) if st2 == 200 else None
    segret = [None]

    def seg_cl():
        import http.client
        u = vt["gatewayUrl"] + f"/live/{c0['id']}/" + seg
        conn = http.client.HTTPConnection("127.0.0.1", MS_PORT, timeout=15)
        conn.request("GET", u)
        r = conn.getresponse(); r.read()
        segret[0] = (r.status, r.getheader("Content-Length"))
        return r.status == 200 and r.getheader("Content-Length")
    ok = seg and wait_for("segment", seg_cl, 30)
    check("live segment has Content-Length", ok, f"seg={seg} got={segret[0]}")

    # recordings appear on both servers
    now = now_ms()

    def has_spans(cam):
        st, b = timeline(tok, cam["id"], now - 10 * 60_000, now_ms() + 60_000)
        return st == 200 and b.get("spans")
    ok1 = wait_for("spans cam1", lambda: has_spans(cams[0]), 180)
    ok2 = wait_for("spans cam2", lambda: has_spans(cams[1]), 180)
    check("recordings on RS 1 and RS 2 (timeline spans)", ok1 and ok2)

    # playback playlist via a playback token
    st, b = timeline(tok, cams[0]["id"], now - 10 * 60_000, now_ms() + 60_000)
    sp = b["spans"][0]
    check("timeline span carries recorderId", bool(sp.get("recorderId")), str(sp)[:120])
    st, vt = video_token(tok, cams[0]["id"], rights=["playback"])
    frm, to = sp["start"], sp.get("end") or now_ms()
    st2, pl = req("GET", vt["gatewayUrl"] + f"/api/v1/cameras/{cams[0]['id']}/playback.m3u8?from={frm}&to={to}", raw=True)
    check("playback playlist via playback token", st2 == 200 and b"#EXTM3U" in (pl if isinstance(pl, bytes) else b""),
          f"{st2} {str(pl)[:100]}")


# --------------------------------------------------------------- direct ---
def t_direct(tok):
    """Clients go straight to the recording server; the RS verifies the token
    offline with the key from its snapshot, and only for itself."""
    sv = servers(tok)
    rs1, rs2 = sv["RS 1"]["id"], sv["RS 2"]["id"]
    by = cam_by_name(tok)
    cam = by["Cam 5"]   # on RS 1, never moved by these checks
    other = by["Cam 3"]  # also on RS 1
    st, b = req("PATCH", f"/api/v2/recording-servers/{rs1}", {"publicUrl": rs_url(1)}, token=tok)
    check("recording server public URL set", st == 200 and b.get("publicUrl") == rs_url(1), str((st, b))[:160])
    try:
        st, vt = video_token(tok, cam["id"])
        direct = st == 200 and vt["baseUrl"].startswith(rs_url(1) + "/v/")
        check("video token baseUrl points at the recording server", direct, str((st, vt))[:200])
        if not direct:
            return
        tokn = vt["token"]
        last = [None]

        def live_direct():
            st2, pl = req("GET", f"/live/{cam['id']}/index.m3u8", base=vt["baseUrl"], raw=True)
            last[0] = st2
            return st2 == 200 and b"#EXTM3U" in pl
        check("live playlist straight from the recording server", wait_for("direct live", live_direct, 30), str(last[0]))

        now = now_ms()
        st, pv = video_token(tok, cam["id"], rights=["playback"])
        st2, pl = req("GET", f"/api/v1/cameras/{cam['id']}/playback.m3u8?from={now - 120_000}&to={now}", base=pv["baseUrl"], raw=True) \
            if st == 200 else (st, b"")
        check("playback playlist straight from the recording server", st2 == 200 and b"#EXTM3U" in pl, str(st2))

        st2, _ = req("GET", f"/v/{tokn}/live/{cam['id']}/index.m3u8", base=rs_url(2), raw=True)
        check("RS 2 refuses a token issued for RS 1", st2 in (401, 403), str(st2))
        st2, _ = req("GET", f"/v/{tokn}/live/{other['id']}/index.m3u8", base=rs_url(1), raw=True)
        check("RS refuses the token for another camera's path", st2 in (401, 403), str(st2))
        h, p, sig = tokn.split(".")
        forged = h + "." + p + "." + ("A" if sig[0] != "A" else "B") + sig[1:]
        st2, _ = req("GET", f"/v/{forged}/live/{cam['id']}/index.m3u8", base=rs_url(1), raw=True)
        check("RS refuses a token with a bad signature", st2 in (401, 403), str(st2))
        # claims rewritten to name another camera, signature kept: refused
        claims = json.loads(base64.urlsafe_b64decode(p + "=" * (-len(p) % 4)))
        claims["cam"] = other["id"]
        p2 = base64.urlsafe_b64encode(json.dumps(claims, separators=(",", ":")).encode()).decode().rstrip("=")
        st2, _ = req("GET", f"/v/{h}.{p2}.{sig}/live/{other['id']}/index.m3u8", base=rs_url(1), raw=True)
        check("RS refuses a token whose claims were edited", st2 in (401, 403), str(st2))
        st2, _ = req("GET", f"/live/{cam['id']}/index.m3u8", base=rs_url(1), raw=True)
        check("RS serves no video without a token", st2 in (401, 403, 404), str(st2))
        bad = []
        for path in ("/api/v2/cameras", "/api/v1/cameras", f"/api/v2/cameras/{cam['id']}", "/api/v1/events"):
            st2, _ = req("GET", path, base=rs_url(1), raw=True)
            if st2 not in (401, 403, 404, 405):
                bad.append((path, st2))
        check("RS HTTP surface has no camera API", not bad, str(bad))
        body = json.dumps(rs_status(1))
        leaked = [c["name"] for c in by.values() if c["id"] in body or c["name"] in body]
        check("RS health endpoint names no cameras", rs_status(1).get("cameras") is not None and not leaked, str(leaked or body[:160]))
    finally:
        req("PATCH", f"/api/v2/recording-servers/{rs1}", {"publicUrl": ""}, token=tok)


# --------------------------------------------------------------- outage ---
def t_outage(tok):
    by = cam_by_name(tok)
    cam1, cam2 = by["Cam 1"], by["Cam 2"]
    sv = servers(tok)
    rs_cams = {s["id"]: set() for s in sv.values()}
    for c in by.values():
        rs_cams.setdefault(c["recorderId"], set()).add(c["id"])
    t0 = now_ms()
    print("  " + site("stop-one", "ms").strip())
    print("  management server stopped for 90 s; recording servers stay up …")
    time.sleep(25)
    # A recording server restarted while the MS is away runs from its saved
    # snapshot (<data>/link/config.json).
    print("  " + site("stop-one", "rs2").strip())
    t_rs2 = now_ms()
    out = site("start-one", "rs2")
    ok = wait_for("RS 2 back on its saved config",
                  lambda: rs_status(2).get("cameras") == 4 and rs_status(2).get("camerasReady") == 4, 45)
    check("restarted RS records from its saved snapshot without the MS", ok, out.strip()[-200:] + " " + json.dumps(rs_status(2)))
    time.sleep(max(0, 90 - (now_ms() - t0) / 1000))
    pend = [rs_status(i).get("pendingEvents") for i in (1, 2)]
    check("events queue in the outbox while the MS is down", sum(p or 0 for p in pend) > 0, str(pend))
    alive = all(f"rs{i}: running" in site("status") for i in (1, 2))
    check("recording servers alive through the outage", alive)
    # restart MS
    print("  " + site("start-one", "ms").strip().splitlines()[-1])
    ok = wait_for("MS back", lambda: req("GET", "/api/v1/status")[0] == 200, 60)
    tok2 = signin(*ADMIN)
    ok2 = wait_for("both RS reconnected",
                   lambda: all(s.get("online") for n, s in servers(tok2).items()), 120)
    check("management server back, both servers reconnected", ok and ok2)
    t1 = now_ms()
    drained = wait_for("outboxes drained",
                       lambda: rs_status(1).get("pendingEvents") == 0 and rs_status(2).get("pendingEvents") == 0, 120)
    check("outbox replayed and drained after reconnect", drained,
          str([rs_status(i).get("pendingEvents") for i in (1, 2)]))
    evs = events(tok2, t0, t1) or []
    during = {e["cameraId"] for e in evs if t0 <= e.get("createdAt", 0) <= t1}
    per_rs = {n: bool(during & rs_cams.get(s["id"], set())) for n, s in sv.items()}
    check("events raised during the outage reached the MS (both servers)", all(per_rs.values()),
          f"{per_rs}, {len(evs)} events")

    check("no recording gap during the outage (RS 1)",
          wait_for("coverage cam1", lambda: covered(tok2, cam1["id"], t0, t1), 60))
    check("RS 2 recorded across the outage after its restart",
          wait_for("coverage cam2", lambda: covered(tok2, cam2["id"], t_rs2 + 30_000, t1), 60))
    if evs is not None:
        ids = [e["id"] for e in evs]
        check("events have no duplicate ids after replay", len(ids) == len(set(ids)), f"{len(ids)} events")
    else:
        check("events list after outage", False)
    return tok2


# ----------------------------------------------------------------- move ---
def t_move(tok):
    cams = cameras(tok)
    cam1 = next(c for c in cams if c["name"] == "Cam 1")
    sv = servers(tok)
    old = cam1["recorderId"]
    new = next(s["id"] for s in sv.values() if s["id"] != old)
    rev_before = {s["id"]: s.get("configRev", 0) for s in sv.values()}
    now = now_ms()
    st, b = timeline(tok, cam1["id"], now - 15 * 60_000, now)
    before = b.get("spans") if st == 200 else None
    st, b = req("PATCH", f"/api/v2/cameras/{cam1['id']}", {"recorderId": new}, token=tok)
    check("camera move accepted", st == 200, str((st, b))[:120])

    def revs_moved():
        s2 = {s["id"]: s for s in servers(tok).values()}
        return all(s2[i].get("configRev", 0) > rev_before[i] for i in (old, new)) \
            and s2[old].get("cameras") == 3 and s2[new].get("cameras") == 5
    check("both servers got and applied a new snapshot (3 + 5 cameras)", wait_for("new snapshots", revs_moved, 60),
          json.dumps({n: (s.get("configRev"), s.get("cameras")) for n, s in servers(tok).items()}))

    def recording_on_new():
        st, b = timeline(tok, cam1["id"], now_ms() - 60_000, now_ms())
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
            st2, pl = req("GET", vt["gatewayUrl"] + f"/api/v1/cameras/{cam1['id']}/playback.m3u8?from={frm}&to={to}", raw=True)
            check("old footage plays after the move", st2 == 200, str(st2))
    # live token for the old placement must be refused
    st, b = video_token(tok, cam1["id"], rights=["live"], recorder=old)
    check("live for an old placement refused", st == 400, str((st, b))[:120])


# ---------------------------------------------------------------- scope ---
def spec_camera_routes():
    """(method, path) for every v2 route naming a camera, from the served spec."""
    st, raw = req("GET", "/api/v2/openapi.yaml", raw=True)
    routes, path = [], None
    for line in raw.decode().splitlines():
        m = re.match(r"^  (/api/v2/\S+):\s*$", line)
        if m:
            path = m.group(1)
            continue
        m = re.match(r"^    (get|post|put|patch|delete):", line)
        if m and path and "{cameraId}" in path:
            routes.append((m.group(1).upper(), path))
        elif re.match(r"^\S", line):
            path = None
    return routes


def ws_messages(token, seconds, topics="alarm,event,camera,config,activity"):
    """Read the v2 event stream for `seconds`; returns decoded messages (or None)."""
    s = socket.create_connection(("127.0.0.1", MS_PORT), timeout=10)
    key = base64.b64encode(os.urandom(16)).decode()
    s.sendall((f"GET /api/v2/events/stream?topics={topics} HTTP/1.1\r\nHost: 127.0.0.1:{MS_PORT}\r\n"
               f"Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: {key}\r\n"
               f"Sec-WebSocket-Version: 13\r\nAuthorization: Bearer {token}\r\n\r\n").encode())
    buf = b""
    while b"\r\n\r\n" not in buf:
        chunk = s.recv(4096)
        if not chunk:
            return None
        buf += chunk
    head, buf = buf.split(b"\r\n\r\n", 1)
    if b" 101 " not in head.split(b"\r\n")[0]:
        return None
    msgs, end = [], time.time() + seconds
    s.settimeout(1)

    def need(n):
        nonlocal buf
        while len(buf) < n:
            if time.time() > end:
                raise TimeoutError
            try:
                chunk = s.recv(65536)
            except socket.timeout:
                continue
            if not chunk:
                raise EOFError
            buf += chunk
    try:
        while time.time() < end:
            need(2)
            op, ln = buf[0] & 0x0F, buf[1] & 0x7F
            off = 2
            if ln == 126:
                need(4); ln = int.from_bytes(buf[2:4], "big"); off = 4
            elif ln == 127:
                need(10); ln = int.from_bytes(buf[2:10], "big"); off = 10
            need(off + ln)
            payload, buf = buf[off:off + ln], buf[off + ln:]
            if op == 1:
                try:
                    msgs.append(json.loads(payload))
                except Exception:
                    pass
            elif op == 8:
                break
    except (TimeoutError, EOFError, OSError):
        pass
    finally:
        s.close()
    return msgs


def camera_ids_in(obj, out=None):
    out = set() if out is None else out
    if isinstance(obj, dict):
        for k, v in obj.items():
            if k == "cameraId" and isinstance(v, str) and v:
                out.add(v)
            elif k == "cameraIds" and isinstance(v, list):
                out.update(x for x in v if isinstance(x, str))
            else:
                camera_ids_in(v, out)
    elif isinstance(obj, list):
        for v in obj:
            camera_ids_in(v, out)
    return out


def t_scope(tok):
    cams = cameras(tok)
    by = {c["name"]: c for c in cams}
    st, g = req("POST", "/api/v2/camera-groups",
                {"name": "Lobby", "cameraIds": [by["Cam 2"]["id"], by["Cam 4"]["id"]]}, token=tok)
    if st == 409 or (st == 400 and "exists" in str(g)):
        st2, gl = req("GET", "/api/v2/camera-groups", token=tok)
        g = next(x for x in gl["items"] if x["name"] == "Lobby"); st = 201
    check("camera group created", st in (200, 201), str((st, g))[:120])
    for name, role in (("op", "Viewer"), ("op2", "Operator")):
        st, u = req("POST", "/api/v2/users",
                    {"name": name, "password": name + "-pass-123", "role": role,
                     "scope": {"allCameras": False, "groupIds": [g["id"]]}}, token=tok)
        if st in (400, 409) and "exists" in str(u):
            st = 201
        check(f"scoped {role.lower()} created", st in (200, 201), str((st, u))[:120])

    op = signin("op", "op-pass-123")
    op2 = signin("op2", "op2-pass-123")
    scoped = {by["Cam 2"]["id"], by["Cam 4"]["id"]}
    vis = cameras(op)
    check("operator sees only the 2 scoped cameras",
          sorted(c["name"] for c in vis) == ["Cam 2", "Cam 4"], str([c["name"] for c in vis]))

    hidden = by["Cam 3"]["id"]
    st, _ = req("GET", f"/api/v2/cameras/{hidden}", token=op)
    check("hidden camera GET → 404", st == 404, str(st))
    st, b = video_token(op, hidden)
    check("hidden camera video token refused", st in (403, 404), str((st, b))[:120])
    now = now_ms()
    st, _ = timeline(op, hidden, now - 600_000, now)
    check("hidden camera timeline refused", st in (403, 404), str(st))
    st, b = req("GET", f"/api/v2/cameras/{hidden}/snapshot", token=op, raw=True)
    check("hidden camera snapshot refused", st in (403, 404), str(st))

    # Every route in the spec that names a camera, for a hidden camera, as a
    # scoped Operator: 404 (doesn't exist for them) or 403 (role), never more.
    leaks, n = [], 0
    for method, path in spec_camera_routes():
        url = re.sub(r"\{cameraId\}", hidden, path)
        url = re.sub(r"\{[A-Za-z]+\}", "x", url)
        n += 1
        st, b = req(method, url, {} if method != "GET" and method != "DELETE" else None, token=op2, raw=True)
        if st not in (403, 404, 405):
            leaks.append((method, path, st))
    check(f"all {n} camera routes refuse a hidden camera (scoped Operator)", n > 10 and not leaks, str(leaks[:6]))

    # lists leave hidden cameras out
    evs = events(op2, now - 3_600_000, now_ms()) or []
    adm = events(tok, now - 3_600_000, now_ms()) or []
    check("event list only has scoped cameras", {e["cameraId"] for e in evs} <= scoped
          and len({e["cameraId"] for e in adm}) > 2, f"{ {e['cameraId'] for e in evs} - scoped }")
    st, al = req("GET", "/api/v2/alarms?limit=200", token=op2)
    items = al.get("items", []) if st == 200 else None
    check("alarm list only has scoped cameras", items is not None
          and {a.get("cameraId") for a in items} <= scoped, str((st, str(al)[:120])))
    hid_ev = next((e for e in adm if e["cameraId"] == hidden), None)
    if hid_ev:
        st, _ = req("GET", f"/api/v2/events/{hid_ev['id']}", token=op2)
        st2, _ = req("GET", f"/api/v2/events/{hid_ev['id']}/thumbnail.jpg", token=op2, raw=True)
        check("hidden camera's event and thumbnail → 404", st == 404 and st2 == 404, str((st, st2)))
        if hid_ev.get("alarmId"):
            st, _ = req("GET", f"/api/v2/alarms/{hid_ev['alarmId']}", token=op2)
            check("hidden camera's alarm → 404", st == 404, str(st))
    st, b = req("POST", "/api/v2/events/search", {"cameraIds": [hidden], "limit": 50}, token=op2)
    found = camera_ids_in(b) & ({c["id"] for c in cams} - scoped) if st == 200 else set()
    check("event search can't reach a hidden camera", st in (200, 400, 403, 404) and not found, str((st, str(b)[:120])))

    # the live event stream is filtered per connection
    import threading
    got = {}
    th = threading.Thread(target=lambda: got.__setitem__("admin", ws_messages(tok, 25)))
    th.start()
    opmsgs = ws_messages(op2, 25)
    th.join()
    admmsgs = got.get("admin")
    op_ids = camera_ids_in(opmsgs or [])
    adm_ids = camera_ids_in(admmsgs or [])
    check("event stream: admin sees other cameras' messages (control)",
          admmsgs is not None and len(adm_ids - scoped) > 0, f"{len(admmsgs or [])} messages")
    check("event stream: scoped operator gets only scoped cameras",
          opmsgs is not None and op_ids <= scoped, f"{len(opmsgs or [])} messages, extra {op_ids - scoped}")

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
    # An operator-minted token for a visible camera can't open the hidden
    # camera's stream — through the gateway or straight at its RS.
    st, vt = video_token(op, by["Cam 2"]["id"])
    if st == 200:
        st2, _ = req("GET", vt["gatewayUrl"] + f"/live/{hidden}/index.m3u8", raw=True)
        check("operator token doesn't open another camera's stream (gateway)", st2 in (401, 403), str(st2))
        bad = []
        for n_ in (1, 2):
            st2, _ = req("GET", f"/v/{vt['token']}/live/{hidden}/index.m3u8", base=rs_url(n_), raw=True)
            if st2 not in (401, 403):
                bad.append((n_, st2))
        check("operator token doesn't open another camera's stream (direct to RS)", not bad, str(bad))
    # recording servers list for the operator: no camera detail
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
    fwd = {}
    for n, s in sv.items():
        st, b = req("GET", f"/api/v2/audit?serverId={s['id']}&limit=50", token=tok)
        items = b.get("items", []) if st == 200 else []
        fwd[n] = sorted({e["action"] for e in items if e.get("serverId") == s["id"]})
    check("forwarded RS entries in the central audit (per server)",
          all("Applied configuration" in a for a in fwd.values()) and len(fwd) == 2, str(fwd))
    bad = []
    for n, s in sv.items():
        st, b = req("GET", f"/api/v2/recording-servers/{s['id']}/log?lines=200", token=tok, raw=True)
        if st != 200 or not isinstance(b, bytes) or b"control link connected" not in b:
            bad.append((n, st, str(b)[-80:]))
    check("recent log fetched from both recording servers", not bad, str(bad))
    st, b = req("GET", "/api/v2/audit?serverId=&limit=50", token=tok)
    acts = [e["action"] for e in (b.get("items", []) if st == 200 else [])]
    check("log fetch is audited on the MS", "Viewed recording server log" in acts, str(acts[:8]))
    rs1 = sv["RS 1"]["id"]
    st, b = req("POST", "/api/v2/auth/token", {"grantType": "password", "name": "op", "password": "op-pass-123"})
    if st == 200:  # the scope phase made this Viewer
        st, _ = req("GET", f"/api/v2/recording-servers/{rs1}/log?lines=10", token=b["accessToken"], raw=True)
        check("log fetch needs the audit permission", st == 403, str(st))
    # A group-scoped Supervisor has ViewAudit, but the trail and the logs
    # name cameras site-wide: refused; verifying the chains stays open.
    st, g = req("GET", "/api/v2/camera-groups", token=tok)
    lobby = next((x["id"] for x in (g.get("items", []) if st == 200 else []) if x["name"] == "Lobby"), None)
    if lobby:
        req("POST", "/api/v2/users", {"name": "sup", "password": "sup-pass-123", "role": "Supervisor",
                                      "scope": {"allCameras": False, "groupIds": [lobby]}}, token=tok)
        sup = signin("sup", "sup-pass-123")
        codes = {p: req("GET", p, token=sup, raw=True)[0] for p in
                 ("/api/v2/audit", f"/api/v2/recording-servers/{rs1}/log?lines=10", f"/api/v2/system/logs?server={rs1}")}
        vst, _ = req("GET", "/api/v2/audit/verify", token=sup)
        check("scoped Supervisor can't read the site-wide audit trail or logs",
              all(c == 403 for c in codes.values()) and vst == 200, str((codes, vst)))
    st, _ = req("GET", f"/api/v2/recording-servers/{rs1}/log?lines=100000", token=tok, raw=True)
    check("log fetch rejects an absurd line count", st == 400, str(st))


# --------------------------------------------------------------- revoke ---
def linkprobe(*args):
    r = subprocess.run([f"{T}/bin/linkprobe", *args], capture_output=True, text=True, timeout=60)
    return r.returncode, (r.stdout + r.stderr).strip()


def t_revoke(tok):
    ctl = f"https://127.0.0.1:{CTL_PORT}"
    st, b = req("POST", "/api/v2/recording-servers/enrollment-tokens", {"name": "RS 3"}, token=tok)
    check("enrollment token created", st == 201 and b.get("token", "").startswith("sen1."), str((st, b))[:160])
    enroll = b["token"]
    out = site("start-one", "rs3", enroll)
    ok = wait_for("RS 3 online", lambda: servers(tok).get("RS 3", {}).get("online"), 60)
    check("third recording server enrolled and online", ok, out.strip()[-200:])
    rs3 = servers(tok).get("RS 3", {}).get("id")
    if not rs3:
        return
    # the token was burned by that enrollment
    probe = os.path.join(T, "probe")
    shutil.rmtree(probe, ignore_errors=True)
    rc, msg = linkprobe("enroll", "-management", ctl, "-token", enroll, "-data", os.path.join(probe, "reuse"))
    check("enrollment token is single-use", rc == 1 and msg.startswith("refused 401"), msg)
    # a token for another management server (wrong CA pin) is refused by the RS
    secret = enroll.split(".")[1]
    other = "sen1." + secret + "." + base64.urlsafe_b64encode(b"\0" * 32).decode().rstrip("=")
    rc, msg = linkprobe("enroll", "-management", ctl, "-token", other, "-data", os.path.join(probe, "pin"))
    check("enrollment refuses a management server whose CA isn't pinned", rc == 1 and "CA" in msg, msg)
    # A second identity, enrolled by the probe itself (renewing RS 3's own
    # certificate would change the fingerprint the MS pins for it).
    st, b = req("POST", "/api/v2/recording-servers/enrollment-tokens", {"name": "Probe"}, token=tok)
    rc, msg = linkprobe("enroll", "-management", ctl, "-token", b.get("token", "") if st == 201 else "",
                        "-data", os.path.join(probe, "id"))
    check("enrollment with a fresh token", rc == 0 and msg.startswith("ok enrolled"), msg)
    probe_id = msg.split()[-1] if rc == 0 else None
    rc, msg = linkprobe("renew", "-data", os.path.join(probe, "id"))
    check("certificate renewal over mTLS", rc == 0 and msg.startswith("ok renewed"), msg)

    by = cam_by_name(tok)
    cam8 = by["Cam 8"]
    home = cam8["recorderId"]
    st, _ = req("PATCH", f"/api/v2/cameras/{cam8['id']}", {"recorderId": rs3}, token=tok)
    ok = wait_for("Cam 8 on RS 3", lambda: any(c["name"] == "Cam 8" and online(c) and c["recorderId"] == rs3
                                                   for c in cameras(tok)), 90)
    check("camera moved to the new server and online", st == 200 and ok)
    st, vt = video_token(tok, cam8["id"])
    pre = st == 200 and wait_for("RS 3 live", lambda: req("GET", vt["gatewayUrl"] + f"/live/{cam8['id']}/index.m3u8", raw=True)[0] == 200, 45)
    check("live video from the new server", pre)

    st, _ = req("DELETE", f"/api/v2/recording-servers/{rs3}", token=tok)
    check("revoke accepted", st == 204, str(st))
    s3 = servers(tok).get("RS 3", {})
    check("revoked server is offline and marked revoked", s3.get("revoked") and not s3.get("online"), json.dumps(s3)[:200])
    if pre:
        st2, _ = req("GET", vt["gatewayUrl"] + f"/live/{cam8['id']}/index.m3u8", raw=True)
        check("tokens issued for the revoked server stop working", st2 in (401, 403), str(st2))
    st, b = video_token(tok, cam8["id"])
    check("no new video tokens for the revoked server", st == 503, str((st, b))[:120])
    st, _ = req("GET", f"/api/v2/recording-servers/{rs3}/log?lines=10", token=tok, raw=True)
    check("log of a revoked server → 503", st == 503, str(st))

    def refused():
        l = rs_status(3).get("link") or {}
        return l.get("connected") is False and "revoked" in (l.get("error") or "")
    check("revoked server's reconnects are refused (it says why)", wait_for("RS 3 refused", refused, 90),
          json.dumps(rs_status(3).get("link")))
    if probe_id:
        st, _ = req("DELETE", f"/api/v2/recording-servers/{probe_id}", token=tok)
        rc, msg = linkprobe("renew", "-data", os.path.join(probe, "id"))
        check("revoked server can't renew its certificate", st == 204 and rc == 1 and msg.startswith("refused 403"), msg)
    st, b = req("GET", "/api/v2/audit?serverId=&limit=100", token=tok)
    acts = [e["action"] for e in (b.get("items", []) if st == 200 else [])]
    check("enrollment token and revoke are audited",
          "Created enrollment token" in acts and "Revoked recording server" in acts, str(acts[:10]))
    st, b = req("DELETE", f"/api/v2/recording-servers/{rs3}", token=tok)
    check("revoking twice is a no-op", st == 204, str(st))

    # its camera moves back and records again
    st, _ = req("PATCH", f"/api/v2/cameras/{cam8['id']}", {"recorderId": home}, token=tok)
    ok = wait_for("Cam 8 home", lambda: any(c["name"] == "Cam 8" and online(c) and c["recorderId"] == home
                                                for c in cameras(tok)), 90)
    check("camera moved off the revoked server records again", st == 200 and ok)
    site("stop-one", "rs3")


if __name__ == "__main__":
    which = sys.argv[1] if len(sys.argv) > 1 else "all"
    tok = signin(*ADMIN)
    if which in ("all", "basic"):
        t_basic(tok)
    if which in ("all", "direct"):
        t_direct(tok)
    if which in ("all", "outage"):
        tok = t_outage(tok)
    if which in ("all", "move"):
        t_move(tok)
    if which in ("all", "scope"):
        t_scope(tok)
    if which in ("all", "audit"):
        t_audit(tok)
    if which in ("all", "revoke"):
        t_revoke(tok)
    fails = [n for n, ok in results if not ok]
    print(f"\n{len(results) - len(fails)}/{len(results)} checks passed" + (f"; FAILED: {fails}" if fails else ""))
    sys.exit(1 if fails else 0)
