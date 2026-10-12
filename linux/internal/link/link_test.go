package link

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func roundTrip[T any](t *testing.T, in T) T {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("%T: marshal: %v", in, err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%T: unmarshal: %v", in, err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("%T changed in a JSON round trip:\n in  %+v\n out %+v\n json %s", in, in, out, raw)
	}
	return out
}

func TestSnapshotRoundTrip(t *testing.T) {
	roundTrip(t, Snapshot{
		Rev: 42,
		Cameras: []CameraConfig{{
			ID: "07ACA52B-E211-4DD7-BCD0-1CC4B1BDD258", Name: "Front Door", RTSPURL: "rtsp://10.0.0.9/1", SubRTSPURL: "rtsp://10.0.0.9/2",
			Username: "admin", Password: "pä$$ \"quoted\"", Recording: true, RetentionDays: 14, MotionLevel: 3,
			ONVIFURL: "http://10.0.0.9/onvif/device_service", CameraAnalytics: true,
		}, {ID: "B", Name: "Garage", RTSPURL: "rtsp://10.0.0.10/live", SealedPassword: "c2VhbGVk"}},
		DefaultRetentionDays: 7, WebRTCExtraHosts: []string{"192.168.1.60", "nvr.lan"}, HWAccel: "auto", AIDescriptions: true,
		VideoTokenKeys: []string{"11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo="},
	})
	roundTrip(t, Applied{Rev: 42, Errors: map[string]string{"B": "bad camera URL"}})
	roundTrip(t, Applied{Rev: 1})
}

func TestStatusRoundTrip(t *testing.T) {
	roundTrip(t, ServerStatus{
		ServerID: "local", Version: "1.2.3", Time: 1760000000123, ConfigRev: 7,
		Media:        MediaStatus{Running: true, Error: "exit status 1"},
		Capabilities: Capabilities{Detection: true, Objects: true, WebRTC: WebRTCStatus{Enabled: true, ICEPort: 18189, ConfigHosts: []string{"10.0.0.2"}}},
		Cameras:      map[string]CameraStatus{"A": {Ready: true, Readers: 2, Width: 1920, Height: 1080, Analytics: "Connected"}, "B": {}},
	})
	roundTrip(t, Hello{ServerID: "rs-1", Version: "1.0.0", Protocol: ProtocolVersion, LastAppliedRev: 3, Capabilities: Capabilities{Detection: true}, VideoURL: "http://rs.lan:8090"})
}

func TestEventRoundTrip(t *testing.T) {
	for _, e := range []Event{
		{ID: NewID(), Type: EventActivity, CameraID: "A", Time: 1, Ephemeral: true, Activity: &Activity{Kind: "motion"}},
		{ID: "E1", Type: EventDetection, ServerID: "local", CameraID: "A", Time: 2, Detection: &Detection{
			EventID: "E1", Source: SourceCamera, Kind: "Line crossing", Label: "person", Score: 0.875, Box: "[0.1,0.2,0.3,0.4]",
			CreatedAt: 1760000000, At: 1760000000456, MergeSince: 1760000001456, Rule: "Gate", Topic: "tns1:RuleEngine/LineDetector/Crossed",
			Summary: `a line crossing (person, rule "Gate")`,
		}},
		{ID: NewID(), Type: EventThumbnail, CameraID: "A", Time: 3, Thumbnail: &Thumbnail{EventID: "E1", File: "E1.jpg"}},
		{ID: NewID(), Type: EventFrames, CameraID: "A", Time: 4, Ephemeral: true, Frames: &Frames{EventID: "E1", Kind: "Person", JPEGs: [][]byte{{0xff, 0xd8, 0}, {1, 2, 3}}}},
		{ID: NewID(), Type: EventStorageLow, Time: 5, Storage: &Storage{FreePct: 3.25, FreeBytes: 2e9}},
	} {
		roundTrip(t, e)
	}
}

func TestCommandAndResultRoundTrip(t *testing.T) {
	c, err := NewCommand(CmdEvidenceAdopt, EvidenceAdoptArgs{Token: "abc", CaseID: "HG-20261011-00001"})
	if err != nil || len(c.ID) != 32 {
		t.Fatalf("command %+v %v", c, err)
	}
	c = roundTrip(t, c)
	var args EvidenceAdoptArgs
	if err := c.Decode(&args); err != nil || args.CaseID != "HG-20261011-00001" || args.Token != "abc" {
		t.Fatalf("args %+v %v", args, err)
	}
	ok := roundTrip(t, OK(c, ExportResult{Token: "abc", Size: 10, SHA256: "ff"}))
	var res ExportResult
	if err := ok.Decode(&res); err != nil || res.Token != "abc" || ok.Re != c.ID {
		t.Fatalf("result %+v %v", res, err)
	}
	fail := roundTrip(t, Fail(c, CodeBusy, "other exports are running — try again in a moment"))
	err = fail.Decode(&res)
	var ce *CommandError
	if err == nil || !asCommandError(err, &ce) || ce.Code != CodeBusy || !strings.Contains(err.Error(), "other exports") {
		t.Fatalf("failure decoded as %v", err)
	}
	for _, v := range []any{
		RecordingsArgs{CameraID: "A"}, []Segment{{Name: "x.mp4", Start: 1, End: 2, Size: 3, Writing: true}},
		CoversArgs{Items: []CoverQuery{{CameraID: "A", At: 5}}}, CoversResult{Covered: []bool{true, false}},
		RangeArgs{CameraID: "A", From: 1, To: 2},
		Timeline{Spans: []Span{{Start: 1, End: 2, Media: 1.5}}, Pending: []Span{{Start: 3, End: 4}}, Writing: true},
		EvidenceLockArgs{CameraID: "A", At: 9, CaseID: "HG-1"}, EvidenceFile{File: "HG-1.mp4", Size: 1, SHA256: "aa", Start: 1, End: 2, Writing: true},
		FileArgs{File: "HG-1.mp4"}, ExportArgs{CameraID: "A", From: 1, To: 2}, SnapshotArgs{CameraID: "A", At: 3}, SnapshotResult{JPEG: []byte{0xff, 0xd8}},
		StorageInfo{RecordingsDir: "/data/recordings", HasDisk: true, DiskFreeBytes: 1, DiskTotalBytes: 2, RecordingsBytes: 3},
		HWAccelStatus{Available: true, Decode: json.RawMessage(`{"usable":["vaapi"]}`), Cameras: json.RawMessage(`[]`), Detection: json.RawMessage(`{"engine":"cpu"}`)},
	} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		out := reflect.New(reflect.TypeOf(v))
		if err := json.Unmarshal(raw, out.Interface()); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(v, out.Elem().Interface()) {
			t.Fatalf("%T changed: %+v → %+v", v, v, out.Elem().Interface())
		}
	}
}

func asCommandError(err error, target **CommandError) bool {
	ce, ok := err.(*CommandError)
	if ok {
		*target = ce
	}
	return ok
}

func TestEnvelope(t *testing.T) {
	env, err := Wrap(TypeConfigSnapshot, "m1", Snapshot{Rev: 3, HWAccel: "off"})
	if err != nil {
		t.Fatal(err)
	}
	env.Rev = 3
	raw, _ := json.Marshal(env)
	if !strings.HasPrefix(string(raw), `{"type":"config.snapshot","id":"m1","rev":3,"body":{`) {
		t.Fatalf("wire form %s", raw)
	}
	var back Envelope
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	var s Snapshot
	if err := back.Decode(&s); err != nil || s.Rev != 3 || s.HWAccel != "off" {
		t.Fatalf("snapshot %+v %v", s, err)
	}
}

// Every exported field of every link message has an explicit JSON name, so
// renaming a Go field can't silently change the wire format.
func TestAllFieldsHaveJSONTags(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(tp reflect.Type) {
		for tp.Kind() == reflect.Pointer || tp.Kind() == reflect.Slice || tp.Kind() == reflect.Map {
			tp = tp.Elem()
		}
		if tp.Kind() != reflect.Struct || seen[tp] || tp.PkgPath() != reflect.TypeOf(Event{}).PkgPath() {
			return
		}
		seen[tp] = true
		for i := 0; i < tp.NumField(); i++ {
			f := tp.Field(i)
			if !f.IsExported() {
				continue
			}
			if tag := f.Tag.Get("json"); tag == "" || strings.HasPrefix(tag, ",") {
				t.Errorf("%s.%s has no json name", tp.Name(), f.Name)
			}
			walk(f.Type)
		}
	}
	for _, v := range []any{Snapshot{}, Applied{}, ServerStatus{}, Hello{}, Event{}, Command{}, Result{}, Envelope{},
		RecordingsArgs{}, Segment{}, CoversArgs{}, CoversResult{}, RangeArgs{}, Timeline{}, EvidenceLockArgs{}, EvidenceFile{},
		FileArgs{}, ExportArgs{}, ExportResult{}, EvidenceAdoptArgs{}, SnapshotArgs{}, SnapshotResult{}, StorageInfo{}, HWAccelStatus{}} {
		walk(reflect.TypeOf(v))
	}
}
