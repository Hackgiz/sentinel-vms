package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sentinel-linux/internal/evidencepkg"
)

func TestV2EvidencePackage(t *testing.T) {
	s, h, evID, op, viewer, _ := shareFixture(t)

	if r := do(t, h, "GET", "/api/v2/evidence/"+evID+"/package", viewer, ""); r.Code != 403 {
		t.Errorf("viewer: %d", r.Code)
	}
	if r := do(t, h, "GET", "/api/v2/evidence/nope/package", op, ""); r.Code != 404 {
		t.Errorf("unknown: %d", r.Code)
	}

	r := do(t, h, "GET", "/api/v2/evidence/"+evID+"/package", op, "")
	if r.Code != 200 || r.Header().Get("Content-Type") != "application/zip" || !strings.Contains(r.Header().Get("Content-Disposition"), "-package.zip") {
		t.Fatalf("package: %d %v %s", r.Code, r.Header(), r.Body)
	}
	pkg := r.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		files[f.Name], _ = io.ReadAll(rc)
		rc.Close()
	}
	var m evidencepkg.Manifest
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	// The manifest carries the vault's own record.
	_, evs := v2Call(t, h, "GET", "/api/v2/evidence", op, "")
	row := items(t, evs)[0]
	if m.CaseID != row["caseId"] || m.Media.SHA256 != row["sha256"] || m.ExportedBy != "op" || m.LockedBy != "op" ||
		m.Camera.ID != row["cameraId"] || m.Range.FromMs == 0 || m.Title != "Lobby incident" {
		t.Fatalf("manifest: %+v vs %v", m, row)
	}
	media := files[m.Media.File]
	if len(media) == 0 || int64(len(media)) != m.Media.Size || files["player.html"] == nil {
		t.Fatalf("media/player: %d bytes, files %v", len(media), len(files))
	}
	// The vault's lock entry is in the chain and the whole package verifies.
	var locked bool
	for _, e := range m.Chain {
		locked = locked || e.Action == "Locked evidence"
	}
	if !locked {
		t.Errorf("chain lacks the lock entry: %+v", m.Chain)
	}
	if v, err := evidencepkg.Verify(bytes.NewReader(pkg), int64(len(pkg))); err != nil || !v.OK {
		t.Fatalf("verify: %v %+v", err, v)
	}
	if !strings.Contains(auditText(t, s), "Exported evidence package") {
		t.Error("package export not audited")
	}

	// A vault file changed since lock time is never packaged.
	vault := filepath.Join(recOf(s).DataDir, "evidence", m.CaseID+".mp4")
	_ = os.Chmod(vault, 0o640)
	if err := os.WriteFile(vault, []byte("tampered"), 0o640); err != nil {
		t.Fatal(err)
	}
	if r := do(t, h, "GET", "/api/v2/evidence/"+evID+"/package", op, ""); r.Code != 409 {
		t.Errorf("tampered vault: %d %s", r.Code, r.Body)
	}
	_ = os.Remove(vault)
	if r := do(t, h, "GET", "/api/v2/evidence/"+evID+"/package", op, ""); r.Code != 404 {
		t.Errorf("missing vault file: %d", r.Code)
	}
}
