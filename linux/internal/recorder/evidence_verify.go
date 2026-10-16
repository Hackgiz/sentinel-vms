package recorder

import (
	"path/filepath"

	"sentinel-linux/internal/link"
)

// verifyEvidence hashes a vault file again (link.CmdEvidenceVerify).
func (r *Recorder) verifyEvidence(file string) (link.EvidenceVerifyResult, string, string) {
	if !vaultFilePattern.MatchString(file) {
		return link.EvidenceVerifyResult{}, link.CodeBadRequest, "bad evidence file"
	}
	size, sum, err := hashFile(filepath.Join(r.evidenceDir(), file))
	if err != nil {
		return link.EvidenceVerifyResult{}, link.CodeNotFound, "evidence file missing"
	}
	return link.EvidenceVerifyResult{Size: size, SHA256: sum}, "", ""
}
