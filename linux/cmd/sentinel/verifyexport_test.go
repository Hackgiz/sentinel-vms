package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sentinel-linux/internal/evidencepkg"
)

func TestRunVerifyExport(t *testing.T) {
	media := []byte("clip bytes")
	sum := sha256.Sum256(media)
	m := &evidencepkg.Manifest{CaseID: "CASE-9", Media: evidencepkg.MediaInfo{Size: int64(len(media)), SHA256: hex.EncodeToString(sum[:])}}
	var good bytes.Buffer
	if err := evidencepkg.Write(&good, m, bytes.NewReader(media)); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	goodPath := filepath.Join(dir, "good.zip")
	if err := os.WriteFile(goodPath, good.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	// Flip one byte of the stored media (zip.Store keeps it verbatim).
	bad := bytes.Replace(good.Bytes(), media, []byte("clip bytez"), 1)
	badPath := filepath.Join(dir, "bad.zip")
	if err := os.WriteFile(badPath, bad, 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := runVerifyExport(goodPath, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "VERIFIED") {
		t.Errorf("good: %d %q %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := runVerifyExport(badPath, &out, &errOut); code != 1 || !strings.HasPrefix(out.String(), "TAMPERED") {
		t.Errorf("tampered: %d %q", code, out.String())
	}
	errOut.Reset()
	if code := runVerifyExport(filepath.Join(dir, "missing.zip"), &out, &errOut); code != 1 || errOut.Len() == 0 {
		t.Errorf("missing: %d", code)
	}
	garbage := filepath.Join(dir, "g.zip")
	_ = os.WriteFile(garbage, []byte("not a zip"), 0o644)
	if code := runVerifyExport(garbage, &out, &errOut); code != 1 {
		t.Errorf("garbage: %d", code)
	}
}
