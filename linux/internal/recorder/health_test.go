package recorder

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSampleKBPS(t *testing.T) {
	r := &Recorder{}
	r.health.last = map[string]byteSample{}
	t0 := time.Now()
	if k := r.sampleKBPS("c", 1000, t0); k != nil {
		t.Fatalf("first reading has no rate: %v", *k)
	}
	k := r.sampleKBPS("c", 1000+250_000, t0.Add(2*time.Second)) // 250 kB in 2 s = 1000 kbit/s
	if k == nil || *k < 999 || *k > 1001 {
		t.Fatalf("rate %v", k)
	}
	// A reading within a second keeps the last rate and baseline.
	if k2 := r.sampleKBPS("c", 1000+251_000, t0.Add(2500*time.Millisecond)); k2 == nil || *k2 != *k {
		t.Fatalf("quick reading: %v", k2)
	}
	// A counter that went backwards (stream restarted) clears the rate.
	if k3 := r.sampleKBPS("c", 10, t0.Add(5*time.Second)); k3 != nil {
		t.Fatalf("restart: %v", *k3)
	}
}

func TestLastSegmentAge(t *testing.T) {
	r := &Recorder{DataDir: t.TempDir()}
	id := "00000000-0000-4000-8000-000000000001"
	now := time.Now()
	if _, ok := r.lastSegmentAge(id, now); ok {
		t.Fatal("no directory should report none")
	}
	dir := filepath.Join(r.recordingsRoot(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, age := range map[string]time.Duration{"20260101-000000-0.mp4": time.Hour, "20260101-010000-0.mp4": 90 * time.Second, "notes.txt": 0} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(-age)
		_ = os.Chtimes(p, mt, mt)
	}
	age, ok := r.lastSegmentAge(id, now)
	if !ok || age < 89 || age > 91 {
		t.Fatalf("age %d ok=%v", age, ok)
	}
}
