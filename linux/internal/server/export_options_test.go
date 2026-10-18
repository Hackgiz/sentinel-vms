package server

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const blurJSON = `"blurRegions":[{"x0":0.1,"y0":0.1,"x1":0.4,"y1":0.4}]`

func TestWatermarkTextIsComposedServerSide(t *testing.T) {
	at := time.Date(2026, 10, 16, 14, 3, 59, 0, time.FixedZone("x", 3600))
	o := exportOpts{watermark: true}
	if got, want := o.watermarkText("Ann", at, "HG-20261016-00001"), "Exported by Ann | 2026-10-16 13:03 UTC | Case HG-20261016-00001"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got := o.watermarkText("Ann", at, ""); strings.Contains(got, "Case") {
		t.Fatalf("download watermark carries no case: %q", got)
	}
	if (exportOpts{}).watermarkText("Ann", at, "x") != "" {
		t.Fatal("no watermark requested, yet text composed")
	}
	if got := (exportOpts{watermark: true, blur: make([][4]float64, 2)}).auditNote(); got != " [watermark, 2 pixelated regions]" {
		t.Fatalf("audit note %q", got)
	}
	if got := (exportOpts{blur: make([][4]float64, 1)}).auditNote(); got != " [1 pixelated region]" {
		t.Fatalf("audit note %q", got)
	}
	if (exportOpts{}).auditNote() != "" {
		t.Fatal("audit note without options")
	}
}

func TestBumpCaseID(t *testing.T) {
	if got := bumpCaseID("HG-20261016-00009"); got != "HG-20261016-00010" {
		t.Fatal(got)
	}
}

func TestReserveCaseIDsAreDistinct(t *testing.T) {
	s, _, _ := playbackFixture(t)
	a, relA := s.reserveCaseID(t.Context())
	b, relB := s.reserveCaseID(t.Context())
	if a == b {
		t.Fatalf("both reservations got %s", a)
	}
	relA()
	relB()
	if c, rel := s.reserveCaseID(t.Context()); c != a {
		t.Fatalf("released id not reused: %s vs %s", c, a)
	} else {
		rel()
	}
}

func TestV1ExportWithOptions(t *testing.T) {
	s, c, t0 := playbackFixture(t)
	h := s.Handler()
	op := webAs(t, s, "operator", "Operator")
	body := func(extra string) string {
		return fmt.Sprintf(`{"from":%d,"to":%d%s}`, t0.UnixMilli(), t0.Add(20*time.Second).UnixMilli(), extra)
	}
	rec := webDo(t, h, op, "POST", "/api/v1/cameras/"+c.ID+"/export", body(`,"watermark":true,`+blurJSON))
	if rec.Code != 200 {
		t.Fatalf("export %d %s", rec.Code, rec.Body)
	}
	var res struct{ URL string }
	if !strings.Contains(rec.Body.String(), "/api/v1/exports/") {
		t.Fatal(rec.Body)
	}
	res.URL = rec.Body.String()[strings.Index(rec.Body.String(), "/api/v1/exports/"):]
	res.URL = res.URL[:strings.Index(res.URL, `"`)]
	dl := webDo(t, h, op, "GET", res.URL, "")
	for _, want := range []string{"-filter_complex ", "flags=neighbor", "watermark.png"} {
		if !strings.Contains(dl.Body.String(), want) {
			t.Errorf("ffmpeg args lack %q: %s", want, dl.Body)
		}
	}
	logs, _ := s.Store.AuditLog(t.Context(), 20)
	audited := false
	for _, e := range logs {
		audited = audited || (e.Action == "Exported clip" && strings.Contains(e.Detail, "[watermark, 1 pixelated region]"))
	}
	if !audited {
		t.Errorf("export audit lacks the options: %+v", logs)
	}

	// Without options: stream copy, audit detail unchanged.
	rec = webDo(t, h, op, "POST", "/api/v1/cameras/"+c.ID+"/export", body(""))
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "pixelated") {
		t.Fatalf("plain export %d %s", rec.Code, rec.Body)
	}
	// Bad regions are refused.
	for _, bad := range []string{
		`"blurRegions":[{"x0":0.5,"y0":0.1,"x1":0.2,"y1":0.4}]`,
		`"blurRegions":[{"x0":0,"y0":0,"x1":2,"y1":1}]`,
		`"blurRegions":[{"x0":0.1,"y0":0.1,"x1":0.101,"y1":0.4}]`,
		`"blurRegions":[` + strings.Repeat(`{"x0":0.1,"y0":0.1,"x1":0.4,"y1":0.4},`, 8) + `{"x0":0.1,"y0":0.1,"x1":0.4,"y1":0.4}]`,
	} {
		if rec := webDo(t, h, op, "POST", "/api/v1/cameras/"+c.ID+"/export", body(","+bad)); rec.Code != 400 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
}

func TestV2EvidenceAndJobsWithOptions(t *testing.T) {
	s, c, t0 := playbackFixture(t)
	s.exportJobs.pollEvery = 10 * time.Millisecond
	h := s.Handler()
	op := v2Token(t, s, "op", "Operator")

	// Evidence with options: filed, audited with the options.
	code, ev := v2Call(t, h, "POST", "/api/v2/evidence", op,
		fmt.Sprintf(`{"cameraId":%q,"fromMs":%d,"toMs":%d,"title":"t","watermark":true,%s}`, c.ID, t0.UnixMilli(), t0.Add(20*time.Second).UnixMilli(), blurJSON))
	if code != 201 || ev["caseId"] == "" {
		t.Fatalf("evidence: %d %v", code, ev)
	}
	dl := do(t, h, "GET", "/api/v2/evidence/"+ev["id"].(string)+"/download", op, "")
	if !strings.Contains(dl.Body.String(), "-filter_complex ") || !strings.Contains(dl.Body.String(), "watermark.png") {
		t.Fatalf("evidence args: %s", dl.Body)
	}

	// Long export with options, to evidence and to download.
	for _, dest := range []string{"evidence", "download"} {
		code, job := v2Call(t, h, "POST", "/api/v2/export-jobs", op,
			fmt.Sprintf(`{"cameraId":%q,"fromMs":%d,"toMs":%d,"dest":%q,"title":"n","watermark":true,%s}`, c.ID, t0.UnixMilli(), t0.Add(3*time.Minute).UnixMilli(), dest, blurJSON))
		if code != 201 {
			t.Fatalf("%s job: %d %v", dest, code, job)
		}
		waitJobs(t, s)
		_, job = v2Call(t, h, "GET", "/api/v2/export-jobs/"+job["taskId"].(string), op, "")
		if job["state"] != "done" {
			t.Fatalf("%s job: %v", dest, job)
		}
		var body string
		if dest == "download" {
			body = do(t, h, "GET", job["downloadUrl"].(string), op, "").Body.String()
		} else {
			body = do(t, h, "GET", "/api/v2/evidence/"+job["evidenceId"].(string)+"/download", op, "").Body.String()
		}
		if !strings.Contains(body, "-filter_complex ") || !strings.Contains(body, "watermark.png") {
			t.Errorf("%s job args: %s", dest, body)
		}
	}
	logs, _ := s.Store.AuditLog(t.Context(), 50)
	var evAudit, jobAudit bool
	for _, e := range logs {
		evAudit = evAudit || (e.Action == "Locked evidence" && strings.Contains(e.Detail, "clip ") && strings.Contains(e.Detail, "[watermark, 1 pixelated region]"))
		jobAudit = jobAudit || (e.Action == "Started long export" && strings.Contains(e.Detail, "[watermark, 1 pixelated region]"))
	}
	if !evAudit || !jobAudit {
		t.Errorf("audit lacks options (evidence %v, job %v)", evAudit, jobAudit)
	}

	// Validation and the "never client text" rule.
	bads := []string{
		`"watermark":true,"blurRegions":[{"x0":0.5,"y0":0.1,"x1":0.2,"y1":0.4}]`,
		`"blurRegions":[{"x0":-1,"y0":0,"x1":1,"y1":1}]`,
		`"watermarkText":"Property of someone else"`,
		`"watermark":"yes"`,
	}
	for _, bad := range bads {
		ev := fmt.Sprintf(`{"cameraId":%q,"fromMs":%d,"toMs":%d,"title":"t",%s}`, c.ID, t0.UnixMilli(), t0.Add(20*time.Second).UnixMilli(), bad)
		if code, _ := v2Call(t, h, "POST", "/api/v2/evidence", op, ev); code != 400 {
			t.Errorf("evidence %s: %d", bad, code)
		}
		job := fmt.Sprintf(`{"cameraId":%q,"fromMs":%d,"toMs":%d,"dest":"download",%s}`, c.ID, t0.UnixMilli(), t0.Add(3*time.Minute).UnixMilli(), bad)
		if code, _ := v2Call(t, h, "POST", "/api/v2/export-jobs", op, job); code != 400 {
			t.Errorf("job %s: %d", bad, code)
		}
	}
}
