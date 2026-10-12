package recorder

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/playback"
)

func TestImportEvidenceSteps(t *testing.T) {
	r := New(t.TempDir(), nil, nil, "test", "")
	file := []byte("....ftyp-imported-evidence-bytes")
	sum := sha256.Sum256(file)
	args := link.EvidenceImportArgs{CaseID: "HG-1", Size: int64(len(file)), SHA256: hex.EncodeToString(sum[:])}
	step := func(op string, off int64, data []byte) (link.EvidenceImportResult, string) {
		a := args
		a.Op, a.Offset, a.Data = op, off, data
		res, code, _ := r.importEvidence(a)
		return res, code
	}
	if _, code := step(link.ImportWrite, 5, file[5:]); code != link.CodeBadRequest {
		t.Fatalf("write without a start: %q", code)
	}
	if _, code := step(link.ImportWrite, 0, file[:10]); code != "" {
		t.Fatal(code)
	}
	if _, code := step(link.ImportWrite, 12, file[12:]); code != link.CodeBadRequest {
		t.Fatalf("out of order: %q", code)
	}
	if _, code := step(link.ImportCommit, 0, nil); code != link.CodeBadRequest {
		t.Fatalf("short commit: %q", code)
	}
	step(link.ImportWrite, 0, file[:10])
	step(link.ImportWrite, 10, file[10:])
	if res, code := step(link.ImportCommit, 0, nil); code != "" || !res.Done || res.File.File != "HG-1.mp4" {
		t.Fatalf("commit: %+v %q", res, code)
	}
	dst := filepath.Join(r.EvidenceDir(), "HG-1.mp4")
	if info, err := os.Stat(dst); err != nil || info.Mode().Perm() != 0o440 {
		t.Fatalf("vault file %v (%v)", info, err)
	}
	// The same file again is done at once; a different one is refused.
	if res, code := step(link.ImportWrite, 0, file[:10]); code != "" || !res.Done {
		t.Fatalf("again: %+v %q", res, code)
	}
	other := sha256.Sum256([]byte("other"))
	args.SHA256 = hex.EncodeToString(other[:])
	if _, code := step(link.ImportWrite, 0, file[:10]); code != link.CodeExists {
		t.Fatalf("different file: %q", code)
	}
	if _, code := step(link.ImportWrite, 0, nil); code != link.CodeExists {
		t.Fatalf("different file: %q", code)
	}
	args.CaseID = "../x"
	if _, code := step(link.ImportWrite, 0, file); code != link.CodeBadRequest {
		t.Fatalf("bad case id: %q", code)
	}
	entries, _ := os.ReadDir(r.EvidenceDir())
	if len(entries) != 1 {
		t.Fatalf("vault holds %v", entries)
	}
}

// Imported recordings linked across volumes are symbolic links; they list
// with the target's size and times.
func TestImportedRecordingsSymlinks(t *testing.T) {
	r := New(t.TempDir(), nil, nil, "test", "")
	cam := "6F1C2D3E-4A5B-4C6D-8E9F-0A1B2C3D4E5F"
	mac := t.TempDir()
	start := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	name := start.Format("20060102-150405") + "-000000.mp4"
	src := filepath.Join(mac, name)
	if err := os.WriteFile(src, playback.SynthSegment(60, 2), 0o644); err != nil {
		t.Fatal(err)
	}
	end := start.Add(time.Minute)
	_ = os.Chtimes(src, end, end)
	dir := filepath.Join(r.RecordingsRoot(), cam)
	_ = os.MkdirAll(dir, 0o755)
	if err := os.Symlink(src, filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	segs, err := r.listSegments(cam)
	if err != nil || len(segs) != 1 || segs[0].End != end.Unix() || segs[0].Start != start.Unix() {
		t.Fatalf("segments %+v (%v)", segs, err)
	}
	files, err := r.library().Files(cam, start.Add(-time.Minute), end.Add(time.Minute))
	if err != nil || len(files) != 1 || files[0].Index == nil {
		t.Fatalf("library %+v (%v)", files, err)
	}

	// Linking a folder: hard links, once, the source untouched; a folder
	// that isn't there is a per-camera error.
	mac2 := t.TempDir()
	name2 := start.Add(time.Hour).Format("20060102-150405") + "-000000.mp4"
	_ = os.WriteFile(filepath.Join(mac2, name2), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(mac2, "notes.txt"), []byte("x"), 0o644)
	var applied link.Applied
	r.linkImportedRecordings([]link.CameraConfig{{ID: cam, ImportedRecordingsPath: mac2}, {ID: "7A2B3C4D-5E6F-4A7B-8C9D-0E1F2A3B4C5D", ImportedRecordingsPath: "/nonexistent/folder"}}, &applied)
	if _, err := os.Stat(filepath.Join(dir, name2)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err == nil {
		t.Fatal("linked a file that isn't a recording")
	}
	if len(applied.Errors) != 1 || applied.Errors["7A2B3C4D-5E6F-4A7B-8C9D-0E1F2A3B4C5D"] == "" {
		t.Fatalf("errors %v", applied.Errors)
	}
}
