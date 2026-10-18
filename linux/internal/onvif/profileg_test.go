package onvif

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A GetRecordingSearchResults answer in the shape real devices send:
// vendor prefixes, fractional seconds, a time without a zone, two
// recordings (main and sub stream), a metadata track.
const searchResultsXML = `<?xml version="1.0" encoding="UTF-8"?>
<env:Envelope xmlns:env="http://www.w3.org/2003/05/soap-envelope" xmlns:tt="http://www.onvif.org/ver10/schema" xmlns:tse="http://www.onvif.org/ver10/search/wsdl">
<env:Body><tse:GetRecordingSearchResultsResponse><tse:ResultList>
<tt:SearchState>Completed</tt:SearchState>
<tt:RecordingInformation>
  <tt:RecordingToken>SD_DISK_20261001_120000_1</tt:RecordingToken>
  <tt:Source><tt:SourceId>http://cam/onvif/device_service</tt:SourceId><tt:Name>Front &amp; Gate</tt:Name><tt:Location/><tt:Description/><tt:Address/></tt:Source>
  <tt:EarliestRecording>2026-10-01T12:00:00.250Z</tt:EarliestRecording>
  <tt:LatestRecording>2026-10-16T08:59:59Z</tt:LatestRecording>
  <tt:Content>Recording from device</tt:Content>
  <tt:Track><tt:TrackToken>Video0001</tt:TrackToken><tt:TrackType>Video</tt:TrackType><tt:Description>video</tt:Description><tt:DataFrom>2026-10-02T00:00:00Z</tt:DataFrom><tt:DataTo>2026-10-16T08:59:58</tt:DataTo></tt:Track>
  <tt:Track><tt:TrackToken>Meta0001</tt:TrackToken><tt:TrackType>Metadata</tt:TrackType><tt:Description/><tt:DataFrom>2026-10-01T12:00:00Z</tt:DataFrom><tt:DataTo>2026-10-16T08:59:59Z</tt:DataTo></tt:Track>
  <tt:RecordingStatus>Recording</tt:RecordingStatus>
</tt:RecordingInformation>
<tt:RecordingInformation>
  <tt:RecordingToken>SD_DISK_SUB</tt:RecordingToken>
  <tt:EarliestRecording>2026-10-10T00:00:00+02:00</tt:EarliestRecording>
  <tt:LatestRecording>2026-10-11T00:00:00+02:00</tt:LatestRecording>
  <tt:RecordingStatus>Stopped</tt:RecordingStatus>
</tt:RecordingInformation>
<tt:RecordingInformation><tt:Content>no token: ignored</tt:Content></tt:RecordingInformation>
</tse:ResultList></tse:GetRecordingSearchResultsResponse></env:Body></env:Envelope>`

func TestParseRecordingSearchResults(t *testing.T) {
	recs, state := parseRecordingResults(parseXML([]byte(searchResultsXML)))
	if state != "Completed" || len(recs) != 2 {
		t.Fatalf("state %q, %d recordings: %+v", state, len(recs), recs)
	}
	r := recs[0]
	if r.Token != "SD_DISK_20261001_120000_1" || r.SourceName != "Front & Gate" || r.Status != "Recording" || len(r.Tracks) != 2 {
		t.Fatalf("first recording %+v", r)
	}
	if want := time.Date(2026, 10, 1, 12, 0, 0, 250e6, time.UTC); !r.Earliest.Equal(want) {
		t.Errorf("earliest %v, want %v", r.Earliest, want)
	}
	// Coverage comes from the video track; a time without a zone is UTC.
	from, to := r.Coverage()
	if !from.Equal(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)) || !to.Equal(time.Date(2026, 10, 16, 8, 59, 58, 0, time.UTC)) {
		t.Errorf("coverage %v – %v", from, to)
	}
	// Without tracks the recording's own range stands in; offsets convert.
	from, to = recs[1].Coverage()
	if !from.Equal(time.Date(2026, 10, 9, 22, 0, 0, 0, time.UTC)) || !to.Equal(time.Date(2026, 10, 10, 22, 0, 0, 0, time.UTC)) {
		t.Errorf("sub coverage %v – %v", from, to)
	}
}

func TestParseRecordingSummary(t *testing.T) {
	s := parseRecordingSummary(parseXML([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tse:GetRecordingSummaryResponse xmlns:tse="x" xmlns:tt="y"><tse:Summary><tt:DataFrom>2026-10-01T00:00:00Z</tt:DataFrom><tt:DataUntil>2026-10-16T12:30:00Z</tt:DataUntil><tt:NumberRecordings>2</tt:NumberRecordings></tse:Summary></tse:GetRecordingSummaryResponse></s:Body></s:Envelope>`)))
	if s.Recordings != 2 || !s.From.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) || !s.Until.Equal(time.Date(2026, 10, 16, 12, 30, 0, 0, time.UTC)) {
		t.Fatalf("summary %+v", s)
	}
	if !parseONVIFTime("garbage").IsZero() || !parseONVIFTime("").IsZero() {
		t.Error("unreadable times should be zero")
	}
}

func TestEdgeAgainstFakecam(t *testing.T) {
	ctx := context.Background()
	from, to := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	g := newCam()
	g.EnableProfileG(from, to)
	with := httptest.NewServer(g)
	defer with.Close()
	without := httptest.NewServer(newCam())
	defer without.Close()
	dev := with.URL + "/onvif/device_service"

	if ok, err := EdgeRecordingSupported(ctx, dev, "admin", "s3cret&<pw"); err != nil || !ok {
		t.Fatalf("Profile G camera: ok=%v err=%v", ok, err)
	}
	if ok, err := EdgeRecordingSupported(ctx, without.URL+"/onvif/device_service", "admin", "s3cret&<pw"); err != nil || ok {
		t.Fatalf("camera without Profile G: ok=%v err=%v", ok, err)
	}
	if _, err := EdgeRecordingSupported(ctx, dev, "admin", "wrong"); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong password: %v, want ErrAuth", err)
	}
	if _, err := OpenEdge(ctx, without.URL+"/onvif/device_service", "admin", "s3cret&<pw"); !errors.Is(err, ErrNoEdgeRecording) {
		t.Fatalf("OpenEdge without Profile G: %v", err)
	}

	e, err := OpenEdge(ctx, dev, "admin", "s3cret&<pw")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(e.search.Path, "/search_service") || !strings.HasSuffix(e.replay.Path, "/replay_service") {
		t.Fatalf("services %v %v", e.search, e.replay)
	}
	sum, err := e.Summary(ctx)
	if err != nil || sum.Recordings != 1 || !sum.From.Equal(from) || !sum.Until.Equal(to) {
		t.Fatalf("summary %+v %v", sum, err)
	}
	// The fakecam answers "Searching" with the recording, then "Completed"
	// with nothing new: the client must keep what the first round gave.
	recs, err := e.Recordings(ctx)
	if err != nil || len(recs) != 1 || recs[0].Token != "rec1" || len(recs[0].Tracks) != 2 {
		t.Fatalf("recordings %+v %v", recs, err)
	}
	if f, tt := recs[0].Coverage(); !f.Equal(from) || !tt.Equal(to) {
		t.Fatalf("coverage %v – %v", f, tt)
	}
	uri, err := e.ReplayURI(ctx, "rec1")
	if err != nil || !strings.HasPrefix(uri, "rtsp://127.0.0.1:554/replay/rec1") {
		t.Fatalf("replay uri %q %v", uri, err)
	}
	if _, err := e.ReplayURI(ctx, "nope"); err == nil {
		t.Fatal("unknown recording accepted")
	}
}

// Older firmware has no GetServices; the search and replay addresses sit in
// GetCapabilities' Extension.
func TestEdgeServicesFromCapabilities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s := string(b)
		w.Header().Set("Content-Type", "application/soap+xml")
		switch {
		case strings.Contains(s, "GetServices"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><s:Fault><s:Reason><s:Text>Action not supported</s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`)
		case strings.Contains(s, "GetCapabilities"):
			_, _ = io.WriteString(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tt="http://www.onvif.org/ver10/schema"><s:Body><tds:GetCapabilitiesResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl"><tds:Capabilities><tt:Media><tt:XAddr>http://`+r.Host+`/media</tt:XAddr></tt:Media><tt:Extension><tt:Recording><tt:XAddr>http://`+r.Host+`/recording</tt:XAddr></tt:Recording><tt:Search><tt:XAddr>http://`+r.Host+`/search</tt:XAddr></tt:Search><tt:Replay><tt:XAddr>http://`+r.Host+`/replay</tt:XAddr></tt:Replay></tt:Extension></tds:Capabilities></tds:GetCapabilitiesResponse></s:Body></s:Envelope>`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	e, err := OpenEdge(context.Background(), srv.URL+"/onvif/device_service", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if e.search.Path != "/search" || e.replay.Path != "/replay" {
		t.Fatalf("services %v %v", e.search, e.replay)
	}
}
