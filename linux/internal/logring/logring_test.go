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
