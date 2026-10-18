// Package evidencepkg builds and verifies the self-verifying evidence
// package (WAVE8-DETECT-EVIDENCE.md, Stream C): a zip holding the media
// file, manifest.json, any notes and an offline player.html that recomputes
// the media's SHA-256 against the manifest.
//
// The manifest is the vault's own metadata, not a second format: the hash
// and size are the ones recorded when the clip was locked, and the chain
// section carries the site audit entries (store.AuditEntry's fields and the
// same hash scheme) that mention the case, so the package's story can be
// re-checked against the audit log it came from.
package evidencepkg

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Format names the manifest in manifest.json.
const (
	Format  = "sentinel-evidence-package"
	Version = 1
)

// File names inside the package. The media sits at the zip root beside
// them, named <case id>.mp4, so player.html finds it by a relative path.
const (
	ManifestName = "manifest.json"
	PlayerName   = "player.html"
	NotesName    = "notes.txt"
)

//go:embed player.html
var playerTemplate string

const manifestToken = "/*SENTINEL_MANIFEST*/null"

// ChainEntry is one audit entry (store.AuditEntry's fields and hash).
type ChainEntry struct {
	ID           string `json:"id"`
	Time         int64  `json:"time"`
	User         string `json:"user"`
	Area         string `json:"area"`
	Action       string `json:"action"`
	Detail       string `json:"detail"`
	PreviousHash string `json:"previousHash"`
	ChainHash    string `json:"chainHash"`
}

// Hash is the chain hash of e after e.PreviousHash: SHA-256 over
// previous hash, id, time, user, area, action and detail joined by 0x1f.
func (e ChainEntry) Hash() string {
	payload := strings.Join([]string{e.PreviousHash, e.ID, strconv.FormatInt(e.Time, 10), e.User, e.Area, e.Action, e.Detail}, "\x1f")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// Manifest is manifest.json.
type Manifest struct {
	Format     string       `json:"format"`
	Version    int          `json:"version"`
	CaseID     string       `json:"caseId"`
	EvidenceID string       `json:"evidenceId"`
	Title      string       `json:"title"`
	Camera     CameraInfo   `json:"camera"`
	Range      RangeInfo    `json:"range"`
	Media      MediaInfo    `json:"media"`
	Note       string       `json:"note,omitempty"`
	LockedAt   int64        `json:"lockedAt"` // Unix seconds, when the clip was locked
	LockedBy   string       `json:"lockedBy"`
	ExportedBy string       `json:"exportedBy"`
	CreatedAt  int64        `json:"createdAt"` // Unix seconds, when this package was made
	Chain      []ChainEntry `json:"chain"`
}

type CameraInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type RangeInfo struct {
	Label  string `json:"label"`
	FromMs int64  `json:"fromMs"` // 0: unknown
	ToMs   int64  `json:"toMs"`
}

type MediaInfo struct {
	File   string `json:"file"` // relative path inside the package
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// MediaFileName is the media's name inside the package for a case.
func MediaFileName(caseID string) string {
	n := strings.Trim(unsafeName.ReplaceAllString(caseID, "_"), "._")
	if n == "" {
		n = "evidence"
	}
	return n + ".mp4"
}

// Player renders player.html with the manifest embedded (file:// pages
// cannot fetch manifest.json, so the page carries its own copy).
func Player(manifestJSON []byte) []byte {
	// "<" (as in "</script>" or "<!--") must not reach the HTML parser;
	// < is the same character to JSON.
	safe := strings.ReplaceAll(string(manifestJSON), "<", `<`)
	return []byte(strings.Replace(playerTemplate, manifestToken, safe, 1))
}

// Write assembles the package: media is the already-hashed media stream
// (the caller has checked it against m.Media.SHA256).
func Write(w io.Writer, m *Manifest, media io.Reader) error {
	if m.Media.File == "" {
		m.Media.File = MediaFileName(m.CaseID)
	}
	m.Format, m.Version = Format, Version
	if m.Chain == nil {
		m.Chain = []ChainEntry{}
	}
	mj, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	zw := zip.NewWriter(w)
	put := func(name string, body []byte) error {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		_, err = f.Write(body)
		return err
	}
	if err := put(ManifestName, mj); err != nil {
		return err
	}
	if err := put(PlayerName, Player(mj)); err != nil {
		return err
	}
	if m.Note != "" {
		if err := put(NotesName, []byte(m.Note+"\n")); err != nil {
			return err
		}
	}
	mf, err := zw.CreateHeader(&zip.FileHeader{Name: m.Media.File, Method: zip.Store}) // mp4 doesn't compress
	if err != nil {
		return err
	}
	if _, err := io.Copy(mf, media); err != nil {
		return err
	}
	return zw.Close()
}

// Verdict is the outcome of verifying a package.
type Verdict struct {
	OK        bool
	CaseID    string
	File      string
	Expected  string // SHA-256 in the manifest
	Actual    string // SHA-256 of the media as found
	Problems  []string
	ChainSize int
}

// SafeRelPath reports whether p is a plain relative path with no way out
// of the package.
func SafeRelPath(p string) bool {
	if p == "" || strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "" || part == "." {
			return false
		}
	}
	return path.Clean(p) == p
}

// Verify recomputes the package's hashes: the media against the manifest
// (hash and size) and every chain entry's own hash, and cross-checks the
// hash the vault wrote into the chain at lock time. Any problem makes
// the verdict not OK; err is for packages that cannot be read at all.
func Verify(r io.ReaderAt, size int64) (*Verdict, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("not a readable zip: %w", err)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	mf := files[ManifestName]
	if mf == nil {
		return nil, errors.New("manifest.json is missing")
	}
	rc, err := mf.Open()
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(rc, 16<<20))
	rc.Close()
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("manifest.json is not valid: %w", err)
	}
	if m.Format != Format {
		return nil, fmt.Errorf("manifest.json is not a Sentinel evidence manifest (format %q)", m.Format)
	}
	v := &Verdict{CaseID: m.CaseID, File: m.Media.File, Expected: strings.ToLower(m.Media.SHA256), ChainSize: len(m.Chain)}
	bad := func(format string, a ...any) { v.Problems = append(v.Problems, fmt.Sprintf(format, a...)) }

	media := files[m.Media.File]
	switch {
	case !SafeRelPath(m.Media.File):
		bad("the media path %q is not a plain relative path", m.Media.File)
	case media == nil:
		bad("the media file %s is missing from the package", m.Media.File)
	default:
		mr, err := media.Open()
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		n, err := io.Copy(h, mr)
		mr.Close()
		if errors.Is(err, zip.ErrChecksum) { // the bytes no longer match the zip's own CRC
			bad("%s fails the zip's own checksum", m.Media.File)
		} else if err != nil {
			return nil, fmt.Errorf("reading %s: %w", m.Media.File, err)
		}
		v.Actual = hex.EncodeToString(h.Sum(nil))
		if v.Actual != v.Expected {
			bad("SHA-256 of %s is %s, the manifest says %s", m.Media.File, v.Actual, v.Expected)
		}
		if n != m.Media.Size {
			bad("%s is %d bytes, the manifest says %d", m.Media.File, n, m.Media.Size)
		}
	}
	for i, e := range m.Chain {
		if e.Hash() != strings.ToLower(e.ChainHash) {
			bad("audit chain entry %d (%s) does not hash to its recorded chain hash", i+1, e.ID)
		}
	}
	if err := ChainBindsMedia(m.Chain, v.Expected); err != nil {
		bad("%v", err)
	}
	v.OK = len(v.Problems) == 0
	return v, nil
}

var shaQuoted = regexp.MustCompile(`sha256 ([0-9a-f]{16,64})`)

// ChainBindsMedia checks that every hash the vault's audit entries quote
// for this clip agrees with the manifest's: lock entries quote the first 16
// hex digits, the vault's file entries quote all 64.
func ChainBindsMedia(chain []ChainEntry, sha string) error {
	for _, e := range chain {
		if e.Area != "Evidence" {
			continue
		}
		if m := shaQuoted.FindStringSubmatch(e.Detail); m != nil && !strings.HasPrefix(sha, m[1]) {
			return fmt.Errorf("audit entry %s (%s) records a different SHA-256 (%s) than the manifest", e.ID, e.Action, m[1])
		}
	}
	return nil
}

// Summary renders a verdict for the CLI.
func (v *Verdict) Summary() string {
	var b bytes.Buffer
	if v.OK {
		fmt.Fprintf(&b, "VERIFIED  case %s: %s matches SHA-256 %s", v.CaseID, v.File, v.Actual)
		if v.ChainSize > 0 {
			fmt.Fprintf(&b, " (%d audit entries consistent)", v.ChainSize)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "TAMPERED  case %s", v.CaseID)
	for _, p := range v.Problems {
		b.WriteString("\n  - " + p)
	}
	return b.String()
}
