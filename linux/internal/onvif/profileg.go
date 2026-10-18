package onvif

// ONVIF Profile G (WAVE9-REACH.md, stream C): the camera's own recordings
// on its SD card. The search service tells what the camera holds
// (GetRecordingSummary, FindRecordings + GetRecordingSearchResults), the
// replay service hands out an RTSP address to stream a recording from a
// given wall time (GetReplayUri); replay.go relays that stream to ffmpeg
// with the headers ONVIF replay needs.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrNoEdgeRecording means the camera answered but offers no recording
// search and replay services (it is not a Profile G device).
var ErrNoEdgeRecording = errors.New("the camera does not offer ONVIF Profile G recording search and replay")

const (
	nsSearch = "http://www.onvif.org/ver10/search/wsdl"
	nsReplay = "http://www.onvif.org/ver10/replay/wsdl"

	searchNS = ` xmlns:tse="` + nsSearch + `"`
	replayNS = ` xmlns:trp="` + nsReplay + `"`

	// maxSearchRounds bounds GetRecordingSearchResults calls per search; a
	// camera that never reports Completed must not keep us polling.
	maxSearchRounds = 20
)

// EdgeSummary is what GetRecordingSummary reports: the span of all data on
// the camera and how many recordings hold it.
type EdgeSummary struct {
	From       time.Time `json:"from"`
	Until      time.Time `json:"until"`
	Recordings int       `json:"recordings"`
}

// EdgeTrack is one track of a recording (Video, Audio, Metadata).
type EdgeTrack struct {
	Token string    `json:"token"`
	Type  string    `json:"type"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
}

// EdgeRecording is one recording on the camera (usually one per video
// source; an NVR-like device has several).
type EdgeRecording struct {
	Token      string      `json:"token"`
	SourceID   string      `json:"sourceId"`
	SourceName string      `json:"sourceName"`
	Earliest   time.Time   `json:"earliest"`
	Latest     time.Time   `json:"latest"`
	Status     string      `json:"status"`
	Tracks     []EdgeTrack `json:"tracks"`
}

// Coverage is the stretch the recording's video spans: the video track's
// data range when the camera reports it, else the whole recording's. It
// is an outer bound: Profile G search does not list the holes inside.
func (r EdgeRecording) Coverage() (from, to time.Time) {
	for _, t := range r.Tracks {
		if strings.EqualFold(t.Type, "Video") && !t.From.IsZero() && t.To.After(t.From) {
			return t.From, t.To
		}
	}
	return r.Earliest, r.Latest
}

// Edge talks to one camera's search and replay services.
type Edge struct {
	c      *client
	search *url.URL
	replay *url.URL
}

func newEdgeClient(serviceURL, user, pass string) (*client, *url.URL, error) {
	dev, err := url.Parse(serviceURL)
	if err != nil || (dev.Scheme != "http" && dev.Scheme != "https") || dev.Host == "" {
		return nil, nil, fmt.Errorf("invalid ONVIF address %q", serviceURL)
	}
	// Searches long-poll for up to a few seconds (WaitTime).
	return &client{http: newHTTPClient(20 * time.Second), user: strings.TrimSpace(user), pass: pass}, dev, nil
}

// edgeServices finds the search and replay service addresses: GetServices
// first (ONVIF 2.x), then GetCapabilities' Extension (older firmware).
func (c *client) edgeServices(ctx context.Context, dev *url.URL) (search, replay *url.URL, err error) {
	parse := func(x string) *url.URL {
		if u, err := url.Parse(strings.TrimSpace(x)); err == nil && u.Host != "" {
			return u
		}
		return nil
	}
	root, err := c.call(ctx, dev, "http://www.onvif.org/ver10/device/wsdl/GetServices",
		"<tds:GetServices><tds:IncludeCapability>false</tds:IncludeCapability></tds:GetServices>")
	if errors.Is(err, ErrAuth) {
		return nil, nil, err
	}
	if err == nil {
		search, replay = servicesFrom(root, parse)
	}
	if search != nil && replay != nil {
		return search, replay, nil
	}
	root, err = c.call(ctx, dev, "http://www.onvif.org/ver10/device/wsdl/GetCapabilities",
		"<tds:GetCapabilities><tds:Category>All</tds:Category></tds:GetCapabilities>")
	if err != nil {
		return nil, nil, err
	}
	if n := root.first("Search"); n != nil && search == nil {
		search = parse(n.value("XAddr"))
	}
	if n := root.first("Replay"); n != nil && replay == nil {
		replay = parse(n.value("XAddr"))
	}
	if search == nil || replay == nil {
		return nil, nil, ErrNoEdgeRecording
	}
	return search, replay, nil
}

// servicesFrom reads a GetServices answer: each Service names its
// namespace and address.
func servicesFrom(root *node, parse func(string) *url.URL) (search, replay *url.URL) {
	for _, s := range root.all("Service") {
		switch strings.TrimSpace(s.value("Namespace")) {
		case nsSearch:
			search = parse(s.value("XAddr"))
		case nsReplay:
			replay = parse(s.value("XAddr"))
		}
	}
	return search, replay
}

// EdgeRecordingSupported is the cheap capability probe behind a camera's
// edgeRecording flag: one clock sync, then GetServices (and, failing that,
// GetCapabilities). A camera that answers without search and replay
// services is (false, nil); an unreachable one or bad credentials is an
// error.
func EdgeRecordingSupported(ctx context.Context, serviceURL, user, pass string) (bool, error) {
	c, dev, err := newEdgeClient(serviceURL, user, pass)
	if err != nil {
		return false, err
	}
	c.syncClock(ctx, dev)
	if _, _, err := c.edgeServices(ctx, dev); err != nil {
		if errors.Is(err, ErrNoEdgeRecording) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// OpenEdge signs in to the camera and finds its search and replay services.
func OpenEdge(ctx context.Context, serviceURL, user, pass string) (*Edge, error) {
	c, dev, err := newEdgeClient(serviceURL, user, pass)
	if err != nil {
		return nil, err
	}
	c.syncClock(ctx, dev)
	search, replay, err := c.edgeServices(ctx, dev)
	if err != nil {
		return nil, err
	}
	return &Edge{c: c, search: search, replay: replay}, nil
}

func (e *Edge) searchCall(ctx context.Context, op, inner string) (*node, error) {
	return e.c.call(ctx, e.search, nsSearch+"/"+op, "<tse:"+op+searchNS+">"+inner+"</tse:"+op+">")
}

// Summary is GetRecordingSummary.
func (e *Edge) Summary(ctx context.Context) (EdgeSummary, error) {
	root, err := e.searchCall(ctx, "GetRecordingSummary", "")
	if err != nil {
		return EdgeSummary{}, err
	}
	return parseRecordingSummary(root), nil
}

func parseRecordingSummary(root *node) EdgeSummary {
	s := root.first("Summary")
	if s == nil {
		s = root
	}
	n, _ := strconv.Atoi(s.value("NumberRecordings"))
	return EdgeSummary{From: parseONVIFTime(s.value("DataFrom")), Until: parseONVIFTime(s.value("DataUntil")), Recordings: n}
}

// Recordings lists every recording on the camera: FindRecordings over an
// empty scope, then GetRecordingSearchResults until the search completes
// (results arrive incrementally), then EndSearch.
func (e *Edge) Recordings(ctx context.Context) ([]EdgeRecording, error) {
	root, err := e.searchCall(ctx, "FindRecordings",
		"<tse:Scope/><tse:MaxMatches>100</tse:MaxMatches><tse:KeepAliveTime>PT30S</tse:KeepAliveTime>")
	if err != nil {
		return nil, err
	}
	token := root.value("SearchToken")
	if token == "" {
		return nil, errors.New("the camera did not return a recording search token")
	}
	defer func() {
		ectx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = e.searchCall(ectx, "EndSearch", "<tse:SearchToken>"+escapeXML(token)+"</tse:SearchToken>")
	}()
	var out []EdgeRecording
	seen := map[string]bool{}
	for round := 0; round < maxSearchRounds; round++ {
		root, err := e.searchCall(ctx, "GetRecordingSearchResults",
			"<tse:SearchToken>"+escapeXML(token)+"</tse:SearchToken><tse:MinResults>1</tse:MinResults><tse:MaxResults>100</tse:MaxResults><tse:WaitTime>PT5S</tse:WaitTime>")
		if err != nil {
			return nil, err
		}
		recs, state := parseRecordingResults(root)
		for _, r := range recs {
			if !seen[r.Token] {
				seen[r.Token] = true
				out = append(out, r)
			}
		}
		if !strings.EqualFold(state, "Queued") && !strings.EqualFold(state, "Searching") {
			return out, nil // Completed, or Unknown: take what came
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// parseRecordingResults reads one GetRecordingSearchResults answer: the
// recordings found so far and the search state.
func parseRecordingResults(root *node) ([]EdgeRecording, string) {
	list := root.first("ResultList")
	if list == nil {
		list = root
	}
	var out []EdgeRecording
	for _, ri := range list.all("RecordingInformation") {
		tok := ri.value("RecordingToken")
		if tok == "" {
			continue
		}
		r := EdgeRecording{
			Token:    tok,
			Earliest: parseONVIFTime(ri.value("EarliestRecording")),
			Latest:   parseONVIFTime(ri.value("LatestRecording")),
			Status:   ri.value("RecordingStatus"),
		}
		if src := ri.first("Source"); src != nil {
			r.SourceID, r.SourceName = src.value("SourceId"), src.value("Name")
		}
		for _, t := range ri.all("Track") {
			r.Tracks = append(r.Tracks, EdgeTrack{
				Token: t.value("TrackToken"), Type: t.value("TrackType"),
				From: parseONVIFTime(t.value("DataFrom")), To: parseONVIFTime(t.value("DataTo")),
			})
		}
		out = append(out, r)
	}
	return out, list.value("SearchState")
}

// ReplayURI is GetReplayUri for one recording: an RTSP address to play it
// back from (with Require: onvif-replay and a clock Range; see replay.go).
func (e *Edge) ReplayURI(ctx context.Context, recordingToken string) (string, error) {
	body := `<trp:GetReplayUri` + replayNS + `><trp:StreamSetup><tt:Stream>RTP-Unicast</tt:Stream><tt:Transport><tt:Protocol>RTSP</tt:Protocol></tt:Transport></trp:StreamSetup><trp:RecordingToken>` +
		escapeXML(recordingToken) + `</trp:RecordingToken></trp:GetReplayUri>`
	root, err := e.c.call(ctx, e.replay, nsReplay+"/GetReplayUri", body)
	if err != nil {
		return "", err
	}
	uri := root.value("Uri")
	if !strings.HasPrefix(strings.ToLower(uri), "rtsp") {
		return "", errors.New("the camera did not return an RTSP replay address for recording " + recordingToken)
	}
	return uri, nil
}

// parseONVIFTime reads an xs:dateTime. Times without a zone are taken as
// UTC (ONVIF devices keep UTC); unreadable values are the zero time.
func parseONVIFTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
