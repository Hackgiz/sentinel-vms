package tasks

import (
	"errors"
	"testing"
	"time"
)

func TestLifecycleAndRetention(t *testing.T) {
	r := New()
	clock := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	r.SetClock(func() time.Time { return clock })

	a := r.Start(KindDiscovery, "Scan")
	clock = clock.Add(time.Second)
	b := r.Start(KindImport, "Import")
	l := r.List()
	if len(l) != 2 || l[0].Title != "Import" || l[0].State != Running || l[0].Progress != Indeterminate {
		t.Fatalf("list: %+v", l)
	}
	b.Progress(40)
	b.Progress(500) // clamped below 100
	a.Fail(errors.New("boom"))
	a.Finish() // does not overwrite the failure
	b.Finish()
	b.Progress(10) // ignored once finished
	got := map[string]Task{}
	for _, x := range r.List() {
		got[x.Title] = x
	}
	if got["Scan"].State != Failed || got["Scan"].Error != "boom" || got["Scan"].FinishedAt == 0 {
		t.Fatalf("scan: %+v", got["Scan"])
	}
	if got["Import"].State != Done || got["Import"].Progress != 100 {
		t.Fatalf("import: %+v", got["Import"])
	}

	c := r.Start(KindExport, "Export") // still running at +16 min: kept
	clock = clock.Add(16 * time.Minute)
	l = r.List()
	if len(l) != 1 || l[0].Title != "Export" {
		t.Fatalf("finished tasks should expire after 15 min, running ones stay: %+v", l)
	}
	c.Finish()
	var nilHandle *Handle
	nilHandle.Progress(1)
	nilHandle.Finish()
	nilHandle.Fail(nil)
}

func TestBounded(t *testing.T) {
	r := New()
	for i := 0; i < maxTasks+50; i++ {
		r.Start(KindExport, "x").Finish()
	}
	if n := len(r.List()); n > maxTasks {
		t.Fatalf("registry unbounded: %d", n)
	}
}
