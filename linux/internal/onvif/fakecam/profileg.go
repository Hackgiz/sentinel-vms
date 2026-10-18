package fakecam

// ONVIF Profile G (WAVE9-REACH.md, stream C): an SD card holding one
// synthetic recording ("rec1"). EnableProfileG switches it on; the camera
// then lists search and replay services in GetServices, answers
// GetRecordingSummary / FindRecordings / GetRecordingSearchResults /
// EndSearch / GetReplayUri, and ServeReplay runs a minimal RTSP replay
// server (Digest auth; PLAY demands Require: onvif-replay and a clock
// Range) that sends a few interleaved packets and ends the stream, so the
// whole recover path runs in tests. The state lives beside the Camera
// (keyed by pointer) so this file needs no field in fakecam.go.

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type profileG struct {
	mu         sync.Mutex
	from, to   time.Time
	replayAddr string
	log        []string
	rounds     map[string]int // search token → result calls
	searches   int
}

var profileGs sync.Map // *Camera → *profileG

// EnableProfileG makes the camera a Profile G device whose card holds one
// recording ("rec1", video + audio tracks) covering [from, to).
func (c *Camera) EnableProfileG(from, to time.Time) {
	profileGs.Store(c, &profileG{from: from.UTC(), to: to.UTC(), rounds: map[string]int{}})
}

func (c *Camera) profileG() *profileG {
	if v, ok := profileGs.Load(c); ok {
		return v.(*profileG)
	}
	return nil
}

// ReplayLog returns what the replay server saw: "PLAY <Range> rate-control=<v>"
// per accepted PLAY, "REJECT <reason>" per refused request.
func (c *Camera) ReplayLog() []string {
	g := c.profileG()
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.log...)
}

func (g *profileG) note(s string) {
	g.mu.Lock()
	g.log = append(g.log, s)
	g.mu.Unlock()
}

const (
	pgSearchNS = "http://www.onvif.org/ver10/search/wsdl"
	pgReplayNS = "http://www.onvif.org/ver10/replay/wsdl"
)

func pgTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// serveProfileG answers a Profile G request s and reports whether it was one.
func (c *Camera) serveProfileG(w http.ResponseWriter, r *http.Request, s string) bool {
	g := c.profileG()
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	tse := ` xmlns:tse="` + pgSearchNS + `"`
	switch {
	case strings.Contains(s, "GetServices"):
		svc := func(ns, path string) string {
			return `<tds:Service><tds:Namespace>` + ns + `</tds:Namespace><tds:XAddr>http://` + r.Host + path + `</tds:XAddr><tds:Version><tt:Major>2</tt:Major><tt:Minor>40</tt:Minor></tds:Version></tds:Service>`
		}
		reply(w, `<tds:GetServicesResponse>`+
			svc("http://www.onvif.org/ver10/device/wsdl", "/onvif/device_service")+
			svc("http://www.onvif.org/ver10/media/wsdl", "/onvif/media_service")+
			svc(pgSearchNS, "/onvif/search_service")+
			svc(pgReplayNS, "/onvif/replay_service")+
			`</tds:GetServicesResponse>`)
	case strings.Contains(s, "GetRecordingSummary"):
		reply(w, `<tse:GetRecordingSummaryResponse`+tse+`><tse:Summary><tt:DataFrom>`+pgTime(g.from)+`</tt:DataFrom><tt:DataUntil>`+pgTime(g.to)+`</tt:DataUntil><tt:NumberRecordings>1</tt:NumberRecordings></tse:Summary></tse:GetRecordingSummaryResponse>`)
	case strings.Contains(s, "FindRecordings"):
		g.searches++
		tok := fmt.Sprintf("search-%d", g.searches)
		g.rounds[tok] = 0
		reply(w, `<tse:FindRecordingsResponse`+tse+`><tse:SearchToken>`+tok+`</tse:SearchToken></tse:FindRecordingsResponse>`)
	case strings.Contains(s, "GetRecordingSearchResults"):
		tok := between(s, "<tse:SearchToken>", "</tse:SearchToken>")
		n, ok := g.rounds[tok]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			reply(w, `<s:Fault><s:Reason><s:Text>unknown search token</s:Text></s:Reason></s:Fault>`)
			return true
		}
		g.rounds[tok] = n + 1
		// Like real devices, results come incrementally: the recording in
		// the first answer (still searching), nothing new in the second.
		state, info := "Completed", ""
		if n == 0 {
			state = "Searching"
			info = `<tt:RecordingInformation><tt:RecordingToken>rec1</tt:RecordingToken><tt:Source><tt:SourceId>vs1</tt:SourceId><tt:Name>` + xmlEscape(c.Model) + `</tt:Name><tt:Location/><tt:Description/><tt:Address/></tt:Source>` +
				`<tt:EarliestRecording>` + pgTime(g.from) + `</tt:EarliestRecording><tt:LatestRecording>` + pgTime(g.to) + `</tt:LatestRecording><tt:Content>SD card</tt:Content>` +
				`<tt:Track><tt:TrackToken>VIDEO001</tt:TrackToken><tt:TrackType>Video</tt:TrackType><tt:Description/><tt:DataFrom>` + pgTime(g.from) + `</tt:DataFrom><tt:DataTo>` + pgTime(g.to) + `</tt:DataTo></tt:Track>` +
				`<tt:Track><tt:TrackToken>AUDIO001</tt:TrackToken><tt:TrackType>Audio</tt:TrackType><tt:Description/><tt:DataFrom>` + pgTime(g.from) + `</tt:DataFrom><tt:DataTo>` + pgTime(g.to) + `</tt:DataTo></tt:Track>` +
				`<tt:RecordingStatus>Recording</tt:RecordingStatus></tt:RecordingInformation>`
		}
		reply(w, `<tse:GetRecordingSearchResultsResponse`+tse+`><tse:ResultList><tt:SearchState>`+state+`</tt:SearchState>`+info+`</tse:ResultList></tse:GetRecordingSearchResultsResponse>`)
	case strings.Contains(s, "EndSearch"):
		delete(g.rounds, between(s, "<tse:SearchToken>", "</tse:SearchToken>"))
		reply(w, `<tse:EndSearchResponse`+tse+`><tse:Endpoint>`+pgTime(time.Now())+`</tse:Endpoint></tse:EndSearchResponse>`)
	case strings.Contains(s, "GetReplayUri"):
		if between(s, "<trp:RecordingToken>", "</trp:RecordingToken>") != "rec1" {
			w.WriteHeader(http.StatusBadRequest)
			reply(w, `<s:Fault><s:Reason><s:Text>no such recording</s:Text></s:Reason></s:Fault>`)
			return true
		}
		addr := g.replayAddr
		if addr == "" {
			host, _, _ := net.SplitHostPort(r.Host)
			addr = net.JoinHostPort(host, "554")
		}
		reply(w, `<trp:GetReplayUriResponse xmlns:trp="`+pgReplayNS+`"><trp:Uri>rtsp://`+addr+`/replay/rec1</trp:Uri></trp:GetReplayUriResponse>`)
	default:
		return false
	}
	return true
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	rest := s[i+len(a):]
	if j := strings.Index(rest, b); j >= 0 {
		return rest[:j]
	}
	return ""
}

// MARK: - RTSP replay

const pgRealm, pgNonce = "fakecam", "5a1f0c3d"

// ServeReplay runs the RTSP replay server on ln until it is closed;
// GetReplayUri then points at it. Call after EnableProfileG.
func (c *Camera) ServeReplay(ln net.Listener) {
	g := c.profileG()
	if g == nil {
		return
	}
	g.mu.Lock()
	g.replayAddr = ln.Addr().String()
	g.mu.Unlock()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go c.serveReplayConn(g, conn)
		}
	}()
}

func (c *Camera) serveReplayConn(g *profileG, conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	for {
		method, uri, hdr, err := readRequest(br)
		if err != nil {
			return
		}
		cseq := hdr["cseq"]
		send := func(status string, extra ...string) {
			var b strings.Builder
			b.WriteString("RTSP/1.0 " + status + "\r\nCSeq: " + cseq + "\r\n")
			for _, e := range extra {
				b.WriteString(e + "\r\n")
			}
			b.WriteString("\r\n")
			_, _ = conn.Write([]byte(b.String()))
		}
		if method == "OPTIONS" {
			send("200 OK", "Public: OPTIONS, DESCRIBE, SETUP, PLAY, TEARDOWN")
			continue
		}
		if !c.digestOK(method, uri, hdr["authorization"]) {
			send("401 Unauthorized", `WWW-Authenticate: Basic realm="`+pgRealm+`"`, `WWW-Authenticate: Digest realm="`+pgRealm+`", nonce="`+pgNonce+`"`)
			continue
		}
		base := "rtsp://" + g.replayAddrLocked() + "/replay/rec1"
		switch method {
		case "DESCRIBE":
			if !strings.HasPrefix(uri, base) {
				g.note("REJECT describe " + uri)
				send("404 Not Found")
				continue
			}
			sdp := "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=Replay\r\nt=0 0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=control:trackID=1\r\n"
			var b strings.Builder
			b.WriteString("RTSP/1.0 200 OK\r\nCSeq: " + cseq + "\r\nContent-Base: " + base + "/\r\nContent-Type: application/sdp\r\n")
			fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n%s", len(sdp), sdp)
			_, _ = conn.Write([]byte(b.String()))
		case "SETUP":
			send("200 OK", "Session: 4711;timeout=60", "Transport: RTP/AVP/TCP;unicast;interleaved=0-1")
		case "PLAY":
			rng := hdr["range"]
			switch {
			case hdr["require"] != "onvif-replay":
				g.note("REJECT no Require: onvif-replay")
				send("551 Option not supported", "Unsupported: onvif-replay")
				continue
			case !strings.HasPrefix(rng, "clock="):
				g.note("REJECT range " + rng)
				send("457 Invalid Range")
				continue
			}
			g.note("PLAY " + rng + " rate-control=" + hdr["rate-control"])
			send("200 OK", "Session: 4711", "Range: "+rng)
			for i := 0; i < 3; i++ { // a few RTP packets, then the recording ends
				_, _ = conn.Write([]byte{'$', 0, 0, 4, 'r', 't', 'p', byte('0' + i)})
			}
			return
		case "TEARDOWN":
			send("200 OK")
			return
		default:
			send("405 Method Not Allowed")
		}
	}
}

func (g *profileG) replayAddrLocked() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.replayAddr
}

// readRequest reads one RTSP request; header names are lower-cased.
func readRequest(br *bufio.Reader) (method, uri string, hdr map[string]string, err error) {
	hdr = map[string]string{}
	line := ""
	for line == "" {
		if line, err = br.ReadString('\n'); err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 {
		return "", "", nil, fmt.Errorf("bad request line %q", line)
	}
	method, uri = parts[0], parts[1]
	for {
		var l string
		if l, err = br.ReadString('\n'); err != nil {
			return
		}
		l = strings.TrimRight(l, "\r\n")
		if l == "" {
			return
		}
		if k, v, ok := strings.Cut(l, ":"); ok {
			hdr[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
}

// digestOK checks an RFC 2617 Digest answer (with or without qop).
func (c *Camera) digestOK(method, uri, authz string) bool {
	if !strings.HasPrefix(strings.ToLower(authz), "digest ") {
		return false
	}
	p := map[string]string{}
	for _, part := range strings.Split(authz[len("digest "):], ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
			p[strings.ToLower(k)] = strings.Trim(v, `"`)
		}
	}
	h := func(s string) string { sum := md5.Sum([]byte(s)); return hex.EncodeToString(sum[:]) }
	if p["username"] != c.User || p["realm"] != pgRealm || p["nonce"] != pgNonce || p["uri"] != uri {
		return false
	}
	ha1, ha2 := h(c.User+":"+pgRealm+":"+c.Pass), h(method+":"+p["uri"])
	want := h(ha1 + ":" + pgNonce + ":" + ha2)
	if p["qop"] != "" {
		want = h(ha1 + ":" + pgNonce + ":" + p["nc"] + ":" + p["cnonce"] + ":" + p["qop"] + ":" + ha2)
	}
	return p["response"] == want
}
