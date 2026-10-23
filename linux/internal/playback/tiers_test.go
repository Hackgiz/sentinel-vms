package playback

// WAVE22: full-res and low-res tiers in one plan.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tierLib: a library with the camera's full-res folder and its low-res
// (<root>/.lowres/CAM-sub) and buffer (<root>/.buffer/CAM) folders.
func tierLib(t *testing.T) (l *Library, full, low, buffer string) {
	t.Helper()
	root := t.TempDir()
	full = filepath.Join(root, "CAM")
	low = filepath.Join(root, LowResDir, "CAM"+LowResSuffix)
	buffer = filepath.Join(root, BufferDir, "CAM")
	for _, d := range []string{full, low, buffer} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &Library{Root: root, Loc: time.UTC}, full, low, buffer
}

type seg struct {
	low       bool
	start     time.Duration // after t0
	secs, gop int
}

type span struct {
	start, end time.Duration // after t0
	low        bool
}

func TestBuildPlanMergesTiers(t *testing.T) {
	ms := time.Millisecond
	cases := []struct {
		name     string
		segs     []seg
		from, to time.Duration // after t0
		want     []span
	}{
		{"full only", []seg{{false, 0, 60, 2}}, -time.Hour, time.Hour,
			[]span{{0, 60 * time.Second, false}}},
		{"low only", []seg{{true, 0, 60, 2}}, -time.Hour, time.Hour,
			[]span{{0, 60 * time.Second, true}}},
		{"low only, window starts mid-GOP: back up to the keyframe (maxPreroll)", []seg{{true, 0, 60, 5}}, 7 * time.Second, time.Hour,
			[]span{{5 * time.Second, 60 * time.Second, true}}},
		{"full inside low", []seg{{true, 0, 120, 2}, {false, 30 * time.Second, 30, 2}}, -time.Hour, time.Hour,
			[]span{{0, 30 * time.Second, true}, {30 * time.Second, 60 * time.Second, false}, {60 * time.Second, 120 * time.Second, true}}},
		{"full inside low, window starts inside the full run", []seg{{true, 0, 120, 2}, {false, 30 * time.Second, 30, 2}}, 40 * time.Second, 100 * time.Second,
			[]span{{40 * time.Second, 60 * time.Second, false}, {60 * time.Second, 100 * time.Second, true}}},
		{"adjacent: full then low", []seg{{false, 0, 60, 2}, {true, 60 * time.Second, 60, 2}}, -time.Hour, time.Hour,
			[]span{{0, 60 * time.Second, false}, {60 * time.Second, 120 * time.Second, true}}},
		{"adjacent: low then full", []seg{{true, 0, 60, 2}, {false, 60 * time.Second, 60, 2}}, -time.Hour, time.Hour,
			[]span{{0, 60 * time.Second, true}, {60 * time.Second, 120 * time.Second, false}}},
		{"full covers everything: low never plays", []seg{{true, 10 * time.Second, 30, 2}, {false, 0, 60, 2}}, -time.Hour, time.Hour,
			[]span{{0, 60 * time.Second, false}}},
		// Full-res starts mid-fragment of the low-res file: low-res keeps the
		// fragment holding the full-res start (overlap, never a hole).
		{"overlapping edge before full-res", []seg{{true, 0, 120, 2}, {false, 50*time.Second + 500*ms, 20, 2}}, -time.Hour, time.Hour,
			[]span{{0, 51 * time.Second, true}, {50*time.Second + 500*ms, 70*time.Second + 500*ms, false}, {70 * time.Second, 120 * time.Second, true}}},
		// Low-res keyframes every 5 s; full-res ends at 32 s. The next
		// low-res keyframe (35 s) would leave 3 s out, so it starts at the
		// one before (30 s): 2 s shown twice.
		{"keyframe cut: overlap beats a gap", []seg{{true, 0, 60, 5}, {false, 0, 32, 2}}, -time.Hour, time.Hour,
			[]span{{0, 32 * time.Second, false}, {30 * time.Second, 60 * time.Second, true}}},
		// Full-res ends at 34 s: the keyframe at 35 s is within the slack.
		{"keyframe cut: first keyframe at/after the gap", []seg{{true, 0, 60, 5}, {false, 0, 34, 2}}, -time.Hour, time.Hour,
			[]span{{0, 34 * time.Second, false}, {35 * time.Second, 60 * time.Second, true}}},
		// Keyframes every 10 s, full-res ends at 27 s: the one before (20 s)
		// is beyond maxOverlap, so the gap stays until 30 s.
		{"keyframe cut: overlap is bounded", []seg{{true, 0, 60, 10}, {false, 0, 27, 2}}, -time.Hour, time.Hour,
			[]span{{0, 27 * time.Second, false}, {30 * time.Second, 60 * time.Second, true}}},
		// A hole of ≤ 1 s between two full-res clips is not filled.
		{"tiny hole between full-res clips", []seg{{true, 0, 120, 2}, {false, 0, 30, 2}, {false, 30*time.Second + 500*ms, 30, 2}}, -time.Hour, time.Hour,
			[]span{{0, 30 * time.Second, false}, {30*time.Second + 500*ms, 60*time.Second + 500*ms, false}, {60 * time.Second, 120 * time.Second, true}}},
		// A 1 ms window (snapshots) on low-res only.
		{"1 ms window on low-res", []seg{{true, 0, 60, 2}}, 21 * time.Second, 21*time.Second + ms,
			[]span{{20 * time.Second, 22 * time.Second, true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, full, low, _ := tierLib(t)
			for _, s := range tc.segs {
				dir := full
				if s.low {
					dir = low
				}
				writeSeg(t, dir, t0.Add(s.start), segmentFile(s.secs, s.gop, 0))
			}
			from, to := t0.Add(tc.from), t0.Add(tc.to)
			files, err := l.Files("CAM", from, to)
			if err != nil {
				t.Fatal(err)
			}
			p := BuildPlan(files, from, to)
			var got []span
			for i, sp := range p.Spans {
				got = append(got, span{sp.Start.Sub(t0), sp.End.Sub(t0), sp.Low})
				if sp.Low != p.Runs[i].File.Low {
					t.Errorf("span %d tier %v, run file tier %v", i, sp.Low, p.Runs[i].File.Low)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("spans %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("spans %+v, want %+v", got, tc.want)
				}
			}
			// Media offsets run on, spans in wall order.
			var media time.Duration
			for i, sp := range p.Spans {
				if sp.Media != media {
					t.Fatalf("span %d media %v, want %v", i, sp.Media, media)
				}
				media += sp.End.Sub(sp.Start)
				if i > 0 && sp.Start.Before(p.Spans[i-1].Start) {
					t.Fatalf("spans out of order %+v", p.Spans)
				}
			}
		})
	}
}

// Every run, whichever tier, gets its own init section and a discontinuity.
func TestPlaylistAcrossTiers(t *testing.T) {
	l, full, low, _ := tierLib(t)
	writeSeg(t, low, t0, segmentFile(120, 2, 0))
	writeSeg(t, full, t0.Add(30*time.Second), segmentFile(30, 2, 0))
	from, to := t0.Add(-time.Hour), t0.Add(time.Hour)
	files, _ := l.Files("CAM", from, to)
	p := BuildPlan(files, from, to)
	if len(p.Runs) != 3 {
		t.Fatalf("runs %+v", p.Spans)
	}
	m3u8 := p.M3U8(func(f *File) (string, bool) {
		if f.Low {
			return "/init/" + f.Name + "?tier=low", false
		}
		return "/recordings/CAM/" + f.Name, true
	}, func(f *File, pc Piece) string {
		return "/p/" + f.Quality() + "/" + f.Name
	})
	lines := strings.Split(m3u8, "\n")
	var maps []string
	tier := ""
	var sum time.Duration
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			maps = append(maps, line)
			if strings.Contains(line, "tier=low") {
				tier = "low"
			} else {
				tier = "full"
			}
			if len(maps) > 1 && lines[i-1] != "#EXT-X-DISCONTINUITY" {
				t.Fatalf("map %d not after a discontinuity:\n%s", len(maps), m3u8)
			}
		case strings.HasPrefix(line, "/p/"):
			if !strings.HasPrefix(line, "/p/"+tier+"/") {
				t.Fatalf("piece %q under a %s init section", line, tier)
			}
		case strings.HasPrefix(line, "#EXTINF:"):
			d, _ := time.ParseDuration(strings.TrimSuffix(strings.TrimPrefix(line, "#EXTINF:"), ",") + "s")
			sum += d
		}
	}
	if len(maps) != 3 || strings.Count(m3u8, "#EXT-X-DISCONTINUITY") != 2 ||
		!strings.Contains(maps[0], "tier=low") || strings.Contains(maps[0], "BYTERANGE") ||
		!strings.Contains(maps[1], "BYTERANGE") || !strings.Contains(maps[2], "tier=low") {
		t.Fatalf("maps %q\n%s", maps, m3u8)
	}
	if sum != p.Duration() || p.Duration() != 2*time.Minute {
		t.Fatalf("EXTINF sum %v, plan %v", sum, p.Duration())
	}
	// Wall → media lands in the right tier.
	if m, ok := p.MediaAt(t0.Add(45 * time.Second)); !ok || m != 45*time.Second {
		t.Fatalf("media at 45 s: %v %v", m, ok)
	}
}

func TestLibraryListsBothTiersAndIgnoresBuffer(t *testing.T) {
	l, full, low, buffer := tierLib(t)
	nf := writeSeg(t, full, t0, segmentFile(10, 2, 0))
	nl := writeSeg(t, low, t0, segmentFile(20, 2, 0))
	writeSeg(t, buffer, t0.Add(5*time.Second), segmentFile(10, 2, 0))
	files, err := l.Files("CAM", t0.Add(-time.Hour), t0.Add(time.Hour))
	if err != nil || len(files) != 2 {
		t.Fatalf("files %v %+v", err, files)
	}
	// Same start: full-res first.
	if files[0].Low || files[0].Name != nf || !files[1].Low || files[1].Name != nl || files[1].Quality() != QualityLow || files[0].Quality() != QualityFull {
		t.Fatalf("files %+v", files)
	}
	if l.PathOf("CAM", &files[1]) != filepath.Join(low, nl) || l.Path("CAM", nf, false) != filepath.Join(full, nf) {
		t.Fatal("paths")
	}
	f, err := l.File("CAM", nl, true)
	if err != nil || !f.Low || len(f.Index.Fragments) != 20 {
		t.Fatalf("low file %v %+v", err, f)
	}
	if _, err := l.File("CAM", nl, false); err != nil { // same name in both tiers: both resolve
		t.Fatal(err)
	}
	if _, err := l.File("CAM", "20260101-000000-000000.mp4", true); err == nil {
		t.Fatal("missing low-res file found")
	}
	// Only a camera without a low-res folder: no error.
	if fs, err := l.Files("OTHER", t0, t0.Add(time.Hour)); err != nil || len(fs) != 0 {
		t.Fatalf("other camera %v %v", err, fs)
	}
	// The cache counts the fragments it holds.
	l.mu.Lock()
	n, frags := len(l.cache), l.frags
	l.mu.Unlock()
	if n != 2 || frags != 30 {
		t.Fatalf("cache %d entries, %d fragments", n, frags)
	}
	l.SetRoot(l.Root)
	if l.frags != 0 {
		t.Fatal("SetRoot kept the fragment count")
	}
}

func TestOneTier(t *testing.T) {
	l, full, low, _ := tierLib(t)
	writeSeg(t, low, t0, segmentFile(120, 2, 0))
	writeSeg(t, full, t0.Add(30*time.Second), segmentFile(30, 2, 0))
	files, _ := l.Files("CAM", t0.Add(-time.Hour), t0.Add(time.Hour))
	tier := func(from, to time.Duration) (full, low int) {
		for _, f := range OneTier(files, t0.Add(from), t0.Add(to)) {
			if f.Low {
				low++
			} else {
				full++
			}
		}
		return
	}
	if f, lo := tier(0, 2*time.Minute); f != 1 || lo != 0 {
		t.Fatalf("mixed range: %d full %d low", f, lo)
	}
	if f, lo := tier(70*time.Second, 100*time.Second); f != 0 || lo != 1 {
		t.Fatalf("low-res only range: %d full %d low", f, lo)
	}
	p, err := ClipPlan(OneTier(files, t0.Add(70*time.Second), t0.Add(100*time.Second)), t0.Add(70*time.Second), t0.Add(100*time.Second))
	if err != nil || len(p.Runs) != 1 || !p.Runs[0].File.Low {
		t.Fatalf("low-res clip plan %v %+v", err, p.Spans)
	}
	// writeRuns reads the low-res file from its own folder.
	names, err := writeRuns(l, "CAM", p, t.TempDir())
	if err != nil || len(names) != 1 {
		t.Fatalf("writeRuns %v %v", err, names)
	}
}
