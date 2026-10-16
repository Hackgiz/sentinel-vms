package link

// CmdEvidenceVerify recomputes the SHA-256 of a file in the recording
// server's evidence vault (FileArgs → EvidenceVerifyResult), so a case can
// be checked against the hash recorded when it was locked. A missing file
// fails with CodeNotFound.
const CmdEvidenceVerify = "evidence.verify"

type EvidenceVerifyResult struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
