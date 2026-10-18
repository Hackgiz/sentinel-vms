package recorder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/link"
)

// loggingFFmpeg logs every call's arguments and writes a fake MP4.
func loggingFFmpeg(t *testing.T) (log string) {
	t.Helper()
	log = filepath.Join(t.TempDir(), "calls.log")
	fakeFFmpegBin(t, `echo "$@" >> '`+log+`'
for last; do :; done
if [ "$last" = "-encoders" ]; then echo " V....D libx264 libx264"; exit 0; fi
echo fake-mp4 > "$last"`)
	return log
}

func TestExportClipCarriesWatermarkAndBlur(t *testing.T) {
	log := loggingFFmpeg(t)
	r, start := longRecorder(t)
	res, code, msg := r.exportClip(context.Background(), link.ExportArgs{CameraID: longCam,
		From: start.UnixMilli(), To: start.Add(20 * time.Second).UnixMilli(),
		Watermark: "Exported by Ann | HG-1", Blur: [][4]float64{{0.1, 0.1, 0.4, 0.4}}})
	if code != "" || res.Token == "" {
		t.Fatal(code, msg)
	}
	b, _ := os.ReadFile(log)
	for _, want := range []string{"-filter_complex ", "flags=neighbor", "watermark.png", "-c:v libx264"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("ffmpeg call lacks %q:\n%s", want, b)
		}
	}
}

func TestExportClipWithoutOptionsStaysStreamCopy(t *testing.T) {
	log := loggingFFmpeg(t)
	r, start := longRecorder(t)
	if _, code, msg := r.exportClip(context.Background(), link.ExportArgs{CameraID: longCam,
		From: start.UnixMilli(), To: start.Add(20 * time.Second).UnixMilli()}); code != "" {
		t.Fatal(code, msg)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "-c copy") || strings.Contains(string(b), "filter") {
		t.Fatalf("expected stream copy:\n%s", b)
	}
}

func TestExportRefusesBadRegions(t *testing.T) {
	loggingFFmpeg(t)
	r, start := longRecorder(t)
	bad := [][][4]float64{
		{{0.5, 0.5, 0.2, 0.9}}, {{0, 0, 1.5, 1}}, {{0.1, 0.1, 0.105, 0.5}},
		make([][4]float64, 9),
	}
	for i, regions := range bad {
		if _, code, _ := r.exportClip(context.Background(), link.ExportArgs{CameraID: longCam,
			From: start.UnixMilli(), To: start.Add(20 * time.Second).UnixMilli(), Blur: regions}); code != link.CodeBadRequest {
			t.Errorf("clip case %d: code %q", i, code)
		}
		if _, code, _ := r.exportLong(link.ExportLongArgs{Op: link.ExportLongOpStart, CameraID: longCam,
			From: start.UnixMilli(), To: start.Add(20 * time.Second).UnixMilli(), Blur: regions}); code != link.CodeBadRequest {
			t.Errorf("long case %d: code %q", i, code)
		}
	}
}

func TestLongExportJobCarriesOptions(t *testing.T) {
	log := loggingFFmpeg(t)
	r, start := longRecorder(t)
	st, code, msg := r.exportLong(link.ExportLongArgs{Op: link.ExportLongOpStart, CameraID: longCam,
		From: start.UnixMilli(), To: start.Add(50 * time.Second).UnixMilli(),
		Watermark: "Exported by Ann", Blur: [][4]float64{{0.1, 0.1, 0.4, 0.4}}})
	if code != "" {
		t.Fatal(code, msg)
	}
	if st = waitLong(t, r, st.JobID); st.State != link.ExportLongDone {
		t.Fatalf("status %+v", st)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "-filter_complex ") || !strings.Contains(string(b), "watermark.png") {
		t.Fatalf("pieces did not carry the options:\n%s", b)
	}
	r.bg.Wait()
}
