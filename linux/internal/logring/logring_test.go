package logring

import (
	"strings"
	"testing"
)

func TestRingKeepsLastLinesRedacted(t *testing.T) {
	r := New(3)
	_, _ = r.Write([]byte("a\nb\nsource=rtsp://admin:hunter2@10.0.0.5:554/s password=hunter2 key=sk-ant-abc123\nremote access ready url=https://foo-bar.trycloudflare.com\npartial"))
	got := r.Text()
	if strings.Contains(got, "hunter2") || strings.Contains(got, "sk-ant-abc123") || strings.Contains(got, "foo-bar") {
		t.Fatalf("secret leaked: %q", got)
	}
	if !strings.Contains(got, "rtsp://***:***@10.0.0.5") || strings.Count(got, "\n") != 2 || strings.HasPrefix(got, "a\n") {
		t.Fatalf("unexpected ring contents: %q", got)
	}
}

func TestTailWrapsAround(t *testing.T) {
	r := New(4)
	for i := 0; i < 10; i++ {
		_, _ = r.Write([]byte{byte('0' + i), '\n'})
	}
	if got := r.Tail(3); got != "7\n8\n9" {
		t.Fatalf("tail 3: %q", got)
	}
	if got := r.Tail(100); got != "6\n7\n8\n9" || r.Text() != got {
		t.Fatalf("tail all: %q", got)
	}
	if New(2).Tail(5) != "" || r.Tail(0) != "" {
		t.Fatal("empty tail")
	}
}
