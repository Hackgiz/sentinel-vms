package evidencepkg

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

func sampleManifest(media []byte) *Manifest {
	sum := sha256.Sum256(media)
	sha := hex.EncodeToString(sum[:])
	lock := ChainEntry{ID: "a1", Time: 100, User: "op", Area: "Evidence", Action: "Locked evidence", Detail: "CASE-1 clip Lobby, sha256 " + sha[:16] + "…"}
	lock.ChainHash = lock.Hash()
	dl := ChainEntry{ID: "a2", Time: 200, User: "op", Area: "Evidence", Action: "Downloaded evidence", Detail: "CASE-1", PreviousHash: lock.ChainHash}
	dl.ChainHash = dl.Hash()
	return &Manifest{CaseID: "CASE-1", EvidenceID: "e1", Title: "Lobby </script> incident", Note: "see note",
		Camera: CameraInfo{ID: "c1", Name: "Lobby"}, Range: RangeInfo{Label: "9:00-9:01", FromMs: 1, ToMs: 2},
		Media: MediaInfo{Size: int64(len(media)), SHA256: sha}, LockedAt: 100, LockedBy: "op", ExportedBy: "boss", CreatedAt: 300,
		Chain: []ChainEntry{lock, dl}}
}

func build(t *testing.T, m *Manifest, media []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Write(&buf, m, bytes.NewReader(media)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// rewrite copies a package, letting fn replace or drop entries.
func rewrite(t *testing.T, pkg []byte, fn func(name string, body []byte) ([]byte, bool)) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		rc, _ := f.Open()
		body, _ := io.ReadAll(rc)
		rc.Close()
		nb, keep := fn(f.Name, body)
		if !keep {
			continue
		}
		w, _ := zw.Create(f.Name)
		_, _ = w.Write(nb)
	}
	_ = zw.Close()
	return out.Bytes()
}

func verify(t *testing.T, pkg []byte) *Verdict {
	t.Helper()
	v, err := Verify(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPackageContentsAndManifest(t *testing.T) {
	media := []byte("fake mp4 bytes")
	pkg := build(t, sampleManifest(media), media)
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		got[f.Name], _ = io.ReadAll(rc)
		rc.Close()
	}
	for _, name := range []string{"manifest.json", "player.html", "notes.txt", "CASE-1.mp4"} {
		if _, ok := got[name]; !ok {
			t.Errorf("package lacks %s (has %d files)", name, len(got))
		}
	}
	if !bytes.Equal(got["CASE-1.mp4"], media) {
		t.Error("media bytes changed")
	}
	for _, want := range []string{`"format": "sentinel-evidence-package"`, `"caseId": "CASE-1"`, `"file": "CASE-1.mp4"`, `"sha256": "` + sampleManifest(media).Media.SHA256, `"exportedBy": "boss"`, `"chain"`} {
		if !strings.Contains(string(got["manifest.json"]), want) {
			t.Errorf("manifest lacks %s:\n%s", want, got["manifest.json"])
		}
	}
	// The player embeds the manifest and cannot be broken out of by its text.
	p := string(got["player.html"])
	if !strings.Contains(p, `"caseId": "CASE-1"`) || strings.Contains(p, manifestToken) {
		t.Error("player does not embed the manifest")
	}
	if strings.Count(p, "</script>") != strings.Count(playerTemplate, "</script>") || strings.Contains(p, "Lobby </script>") {
		t.Error("manifest text not neutralised inside the player")
	}
	if strings.Contains(p, "http://") || strings.Contains(p, "https://") || strings.Contains(p, "<script src") {
		t.Error("player must be fully offline")
	}
	if v := verify(t, pkg); !v.OK || v.ChainSize != 2 {
		t.Fatalf("fresh package: %+v", v)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	media := []byte("fake mp4 bytes")
	pkg := build(t, sampleManifest(media), media)

	t.Run("media bytes", func(t *testing.T) {
		v := verify(t, rewrite(t, pkg, func(n string, b []byte) ([]byte, bool) {
			if n == "CASE-1.mp4" {
				return []byte("fake mp4 bytez"), true
			}
			return b, true
		}))
		if v.OK || !strings.Contains(strings.Join(v.Problems, "\n"), "SHA-256") || !strings.HasPrefix(v.Summary(), "TAMPERED") {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("media missing", func(t *testing.T) {
		v := verify(t, rewrite(t, pkg, func(n string, b []byte) ([]byte, bool) { return b, n != "CASE-1.mp4" }))
		if v.OK || !strings.Contains(v.Problems[0], "missing") {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("manifest hash edited", func(t *testing.T) {
		v := verify(t, rewrite(t, pkg, func(n string, b []byte) ([]byte, bool) {
			if n == "manifest.json" {
				return bytes.Replace(b, []byte(sampleManifest(media).Media.SHA256[:4]), []byte("0000"), 1), true
			}
			return b, true
		}))
		if v.OK {
			t.Fatalf("edited manifest hash passed: %+v", v)
		}
	})
	t.Run("chain entry edited", func(t *testing.T) {
		v := verify(t, rewrite(t, pkg, func(n string, b []byte) ([]byte, bool) {
			if n == "manifest.json" {
				return bytes.Replace(b, []byte(`"user": "op"`), []byte(`"user": "mallory"`), 1), true
			}
			return b, true
		}))
		if v.OK || !strings.Contains(strings.Join(v.Problems, "\n"), "chain") {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("path escape", func(t *testing.T) {
		m := sampleManifest(media)
		m.Media.File = "../x.mp4"
		v := verify(t, build(t, m, media))
		if v.OK {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("not a package", func(t *testing.T) {
		if _, err := Verify(bytes.NewReader([]byte("nope")), 4); err == nil {
			t.Error("garbage verified")
		}
		noManifest := rewrite(t, pkg, func(n string, b []byte) ([]byte, bool) { return b, n != "manifest.json" })
		if _, err := Verify(bytes.NewReader(noManifest), int64(len(noManifest))); err == nil {
			t.Error("package without manifest verified")
		}
	})
}

func TestSafeRelPathAndMediaFileName(t *testing.T) {
	for p, want := range map[string]bool{"a.mp4": true, "dir/a.mp4": true, "": false, "/a": false, "../a": false, "a/../b": false, "a//b": false, `a\b`: false, "./a": false} {
		if SafeRelPath(p) != want {
			t.Errorf("SafeRelPath(%q) != %v", p, want)
		}
	}
	if MediaFileName("CASE 2026/1") != "CASE_2026_1.mp4" || MediaFileName("..") != "evidence.mp4" {
		t.Error(MediaFileName("CASE 2026/1"), MediaFileName(".."))
	}
}

// The same vector the player's vitest uses: the hash is shared by Go, the
// player's JS and `shasum` over the 0x1f-joined fields.
func TestChainHashVector(t *testing.T) {
	e := ChainEntry{ID: "a1", Time: 100, User: "op", Area: "Evidence", Action: "Locked evidence", Detail: "x"}
	if got := e.Hash(); got != "b0002961e5aab684fe9d7366ec72a3fde5211798ad7081c489afdd795f7936cc" {
		t.Errorf("hash %s", got)
	}
}
