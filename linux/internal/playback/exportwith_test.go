package playback

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Hand-written graphs for the pieces of ExportFilterGraph.
const (
	boxA = "drawbox=x=iw*0.05000:y=ih*0.05000:w=iw*0.20000:h=ih*0.20000:color=black:t=fill"
	boxB = "drawbox=x=iw*0.60000:y=ih*0.60000:w=iw*0.20000:h=ih*0.20000:color=black:t=fill"
	wm   = "overlay=10:main_h-overlay_h-10"
)

func pix(in, out string, i, x, y, w, h string) string {
	return "[" + in + "]split=2[pa" + i + "][pb" + i + "];" +
		`[pb` + i + `]crop=w=min(floor(iw/16)*16\,ceil(iw*` + w + `/16)*16):h=min(floor(ih/16)*16\,ceil(ih*` + h + `/16)*16)` +
		`:x=min(iw*` + x + `\,iw-out_w):y=min(ih*` + y + `\,ih-out_h),` +
		`scale=iw/16:ih/16,scale=iw*16:ih*16:flags=neighbor[pp` + i + `];` +
		`[pa` + i + `][pp` + i + `]overlay=x=min(main_w*` + x + `\,main_w-overlay_w):y=min(main_h*` + y + `\,main_h-overlay_h)[` + out + `]`
}

var (
	rg1    = [4]float64{0.1, 0.2, 0.3, 0.5}
	rg2    = [4]float64{0.5, 0.5, 0.75, 0.9}
	maskA  = [4]float64{0.05, 0.05, 0.25, 0.25}
	maskB  = [4]float64{0.6, 0.6, 0.8, 0.8}
	pixRg1 = func(in, out string, i string) string {
		return pix(in, out, i, "0.10000", "0.20000", "0.20000", "0.30000")
	}
	pixRg2 = func(in, out string, i string) string {
		return pix(in, out, i, "0.50000", "0.50000", "0.25000", "0.40000")
	}
)

func TestExportFilterGraph(t *testing.T) {
	masks := [][4]float64{maskA, maskB}
	cases := []struct {
		name  string
		masks [][4]float64
		blur  [][4]float64
		wm    bool
		want  string
	}{
		{"watermark only", nil, nil, true, "[0:v][1:v]" + wm + "[vout]"},
		{"1 region", nil, [][4]float64{rg1}, false, pixRg1("0:v", "vout", "0")},
		{"2 regions", nil, [][4]float64{rg1, rg2}, false, pixRg1("0:v", "v0", "0") + ";" + pixRg2("v0", "vout", "1")},
		{"1 region + wm", nil, [][4]float64{rg1}, true, pixRg1("0:v", "v0", "0") + ";[v0][1:v]" + wm + "[vout]"},
		{"2 regions + wm", nil, [][4]float64{rg1, rg2}, true,
			pixRg1("0:v", "v0", "0") + ";" + pixRg2("v0", "v1", "1") + ";[v1][1:v]" + wm + "[vout]"},
		{"masks + 1 region", masks, [][4]float64{rg1}, false,
			"[0:v]" + boxA + "," + boxB + "[v0];" + pixRg1("v0", "vout", "0")},
		{"masks + 2 regions + wm", masks, [][4]float64{rg1, rg2}, true,
			"[0:v]" + boxA + "," + boxB + "[v0];" + pixRg1("v0", "v1", "0") + ";" + pixRg2("v1", "v2", "1") + ";[v2][1:v]" + wm + "[vout]"},
		{"masks + wm", masks, nil, true, "[0:v]" + boxA + "," + boxB + "[v0];[v0][1:v]" + wm + "[vout]"},
	}
	for _, c := range cases {
		if got := ExportFilterGraph(c.masks, c.blur, c.wm); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, got, c.want)
		}
	}
}

func TestExportFilterGraphUsesOnlyCoreFilters(t *testing.T) {
	g := ExportFilterGraph([][4]float64{maskA}, [][4]float64{rg1, rg2}, true)
	for _, bad := range []string{"drawtext", "boxblur", "gblur", "avgblur", "pixelize"} {
		if strings.Contains(g, bad) {
			t.Errorf("graph uses %s", bad)
		}
	}
	for _, want := range []string{"split=2", "crop=", "scale=iw/16:ih/16", "flags=neighbor", "overlay="} {
		if !strings.Contains(g, want) {
			t.Errorf("graph lacks %s", want)
		}
	}
}

func TestClipArgsWithModes(t *testing.T) {
	enc := encoderX264
	// No options: exactly the stream copy.
	if got, want := ClipArgsWith("l", 0, time.Minute, "o.mp4", true, ExportOptions{}, nil, ""), ClipArgs("l", 0, time.Minute, "o.mp4", true); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("no options:\n%v\n%v", got, want)
	}
	// Masks only: the wave-7 args, unchanged.
	m := ExportOptions{Masks: [][4]float64{maskA}}
	if got, want := ClipArgsWith("l", 0, time.Minute, "o.mp4", true, m, enc, ""), ClipArgsMaskedWith("l", 0, time.Minute, "o.mp4", true, m.Masks, enc); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("masks only:\n%v\n%v", got, want)
	}
	// Watermark + blur: second input, filter_complex, mapped vout, the given encoder.
	o := ExportOptions{Watermark: "x", Blur: [][4]float64{rg1}}
	s := strings.Join(ClipArgsWith("l", 0, time.Minute, "o.mp4", true, o, encoderVideoToolbox, "/w/watermark.png"), " ")
	for _, want := range []string{"-i l -i /w/watermark.png", "-filter_complex ", "-map [vout]", "-map 0:a? -c:a copy", "-c:v h264_videotoolbox", "-pix_fmt yuv420p"} {
		if !strings.Contains(s, want) {
			t.Errorf("args lack %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "-vf") || strings.Contains(s, "libx264") {
		t.Errorf("unexpected -vf / libx264: %s", s)
	}
	// Blur without a watermark has no second input; no audio drops it.
	s = strings.Join(ClipArgsWith("l", 0, time.Minute, "o.mp4", false, ExportOptions{Blur: [][4]float64{rg1}}, enc, ""), " ")
	if strings.Contains(s, "-i /") || strings.Count(s, " -i ") != 1 || !strings.Contains(s, "-an") || strings.Contains(s, "1:v") {
		t.Errorf("blur only: %s", s)
	}
}

func TestRenderWatermark(t *testing.T) {
	a, err := RenderWatermark("Exported by Ann | HG-20261016-00001", 2)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := RenderWatermark("Exported by Ann | HG-20261016-00001", 2)
	if !bytes.Equal(a, b) {
		t.Fatal("render is not deterministic")
	}
	img, err := png.Decode(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	w, h := WatermarkSize("Exported by Ann | HG-20261016-00001", 2)
	if img.Bounds().Dx() != w || img.Bounds().Dy() != h {
		t.Fatalf("size %v, want %dx%d", img.Bounds(), w, h)
	}
	// 35 glyphs * 6 - 1 columns at scale 2, plus padding of 6 on each side.
	if w != 2*6+(35*6-1)*2 || h != 7*2+2*6 {
		t.Fatalf("unexpected geometry %dx%d", w, h)
	}
	// Semi-transparent background, brighter opaque-ish text pixels exist.
	_, _, _, bgA := img.At(0, 0).RGBA()
	if bgA == 0 || bgA == 0xffff {
		t.Fatalf("background alpha %d should be partial", bgA)
	}
	bright := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if r, _, _, _ := img.At(x, y).RGBA(); r > 0xc000 {
				bright++
			}
		}
	}
	if bright < 100 {
		t.Fatalf("only %d text pixels", bright)
	}
	// Different text, different picture; lowercase equals uppercase.
	c, _ := RenderWatermark("Other", 2)
	if bytes.Equal(a, c) {
		t.Fatal("different text rendered identically")
	}
	u, _ := RenderWatermark("ANN", 2)
	l, _ := RenderWatermark("ann", 2)
	if !bytes.Equal(u, l) {
		t.Fatal("lowercase should render as uppercase")
	}
	if WatermarkText("a\x00é") != "A??" {
		t.Fatalf("got %q", WatermarkText("a\x00é"))
	}
	if n := len([]rune(WatermarkText(strings.Repeat("x", 500)))); n != MaxWatermarkText {
		t.Fatalf("not capped: %d", n)
	}
}

const fakeWithEncoders = `for last; do :; done
if [ "$last" = "-encoders" ]; then echo " V....D libx264 libx264"; exit 0; fi
echo fake-mp4 > "$last"`

func TestExportWithPassesOptionsToFFmpeg(t *testing.T) {
	l, dir := lib(t)
	writeSeg(t, dir, t0, segmentFile(60, 2, 0))
	from, to := t0, t0.Add(30*time.Second)
	files, _ := l.Files("CAM", from, to)
	bin, log := fakeFFmpeg(t, t.TempDir(), fakeWithEncoders)
	work, out := t.TempDir(), filepath.Join(t.TempDir(), "o.mp4")
	o := ExportOptions{Masks: [][4]float64{maskA}, Watermark: "Exported by Ann", Blur: [][4]float64{rg1}}
	if err := ExportWith(context.Background(), bin, l, "CAM", files, from, to, work, out, o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "watermark.png")); err != nil {
		t.Fatalf("watermark PNG not written to the work dir: %v", err)
	}
	b, _ := os.ReadFile(log)
	calls := string(b)
	if !strings.Contains(calls, "-filter_complex "+ExportFilterGraph(o.Masks, o.Blur, true)) ||
		!strings.Contains(calls, "watermark.png") || !strings.Contains(calls, "-c:v libx264") {
		t.Fatalf("ffmpeg calls:\n%s", calls)
	}

	// No options: stream copy, no encoder probe, no watermark file.
	bin, log = fakeFFmpeg(t, t.TempDir(), fakeWithEncoders)
	work = t.TempDir()
	if err := ExportWith(context.Background(), bin, l, "CAM", files, from, to, work, filepath.Join(t.TempDir(), "p.mp4"), ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(log)
	if !strings.Contains(string(b), "-c copy") || strings.Contains(string(b), "-encoders") || strings.Contains(string(b), "filter") {
		t.Fatalf("stream copy expected:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(work, "watermark.png")); err == nil {
		t.Fatal("watermark written without the option")
	}

	// ExportMasked stays a masks-only wrapper.
	bin, log = fakeFFmpeg(t, t.TempDir(), fakeWithEncoders)
	if err := ExportMasked(context.Background(), bin, l, "CAM", files, from, to, t.TempDir(), filepath.Join(t.TempDir(), "q.mp4"), [][4]float64{maskA}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(log)
	if !strings.Contains(string(b), "-vf "+DrawboxFilter([][4]float64{maskA})) {
		t.Fatalf("ExportMasked args:\n%s", b)
	}
}

func TestLongExportCarriesOptions(t *testing.T) {
	l, files, from, to := longSetup(t)
	bin, log := fakeFFmpeg(t, t.TempDir(), fakeWithEncoders)
	work, out := t.TempDir(), filepath.Join(t.TempDir(), "long.mp4")
	o := ExportOptions{Watermark: "Exported by Ann", Blur: [][4]float64{rg1}}
	err := LongExport(context.Background(), LongOptions{
		FFmpeg: bin, Lib: l, CameraID: "CAM", Files: files, From: from, To: to, WorkDir: work, Out: out, Export: o,
		FreeBytes: func(string) (uint64, error) { return 1 << 40, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	graph := "-filter_complex " + ExportFilterGraph(nil, o.Blur, true)
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.Contains(line, graph) {
			n++
			if !strings.Contains(line, "-c:v libx264") {
				t.Errorf("piece not encoded through MaskEncoderArgs: %s", line)
			}
		}
	}
	if n != 3 { // 70 minutes at a 30-minute cap
		t.Fatalf("%d pieces carried the options:\n%s", n, b)
	}
	if left, _ := os.ReadDir(work); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

// TestExportGraphRunsInRealFFmpeg runs the generated graphs through a real
// ffmpeg (set SENTINEL_TEST_FFMPEG to its path, e.g. the macOS bundle, to
// prove they use only filters it has). Skipped otherwise.
func TestExportGraphRunsInRealFFmpeg(t *testing.T) {
	ff := os.Getenv("SENTINEL_TEST_FFMPEG")
	if ff == "" {
		t.Skip("SENTINEL_TEST_FFMPEG not set")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	if err := runFFmpeg(context.Background(), ff, []string{"-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=642x362:rate=10", "-t", "2", "-pix_fmt", "yuv420p", "-y", src}); err != nil {
		t.Skipf("cannot make a source clip with this ffmpeg: %v", err)
	}
	list := filepath.Join(dir, "l.ffconcat")
	_ = os.WriteFile(list, []byte("ffconcat version 1.0\nfile 'src.mp4'\n"), 0o600)
	pngB, _ := RenderWatermark("Exported by Ann | HG-20261016-00001", WatermarkScale)
	wmPath := filepath.Join(dir, "wm.png")
	_ = os.WriteFile(wmPath, pngB, 0o600)
	enc := MaskEncoderArgs(context.Background(), ff)
	edge := [4]float64{0.9, 0.9, 1, 1}
	for name, o := range map[string]ExportOptions{
		"blur1":      {Blur: [][4]float64{rg1}},
		"blur2+wm":   {Blur: [][4]float64{rg1, rg2}, Watermark: "Exported by Ann"},
		"edge+masks": {Masks: [][4]float64{maskA}, Blur: [][4]float64{edge, rg2}, Watermark: "x"},
		"wm":         {Watermark: "Exported by Ann"},
	} {
		out := filepath.Join(dir, name+".mp4")
		if err := runFFmpeg(context.Background(), ff, ClipArgsWith(list, 0, time.Second, out, false, o, enc, wmPath)); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if st, err := os.Stat(out); err != nil || st.Size() == 0 {
			t.Errorf("%s: no output", name)
		}
	}
}
