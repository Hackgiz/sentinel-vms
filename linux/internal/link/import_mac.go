package link

// Evidence files imported from a Sentinel for Mac (1.x) install
// (PLATFORM.md, "Decision 1"; PUT /api/v2/import/mac/evidence/{caseId}).
// The management server streams the upload to the recording server in
// chunks of CmdEvidenceImport, so it works the same in-process and over the
// control link (each chunk is one command, well under the message limit).

// CmdEvidenceImport writes one imported evidence file into the vault:
// EvidenceImportArgs → EvidenceImportResult.
const CmdEvidenceImport = "evidence.import"

// CodeExists: the vault already has a different file for that case.
const CodeExists = "exists"

// Evidence import steps (EvidenceImportArgs.Op).
const (
	ImportWrite  = "write"  // Data at Offset into the upload in progress (Offset 0 starts over)
	ImportCommit = "commit" // check Size and SHA256, then move it into the vault read-only
	ImportAbort  = "abort"  // drop the upload in progress
)

// ImportChunk is how many bytes one ImportWrite carries.
const ImportChunk = 4 << 20

// EvidenceImportArgs is one step of an upload. Size and SHA256 (lowercase
// hex) are the file's expected length and hash, sent with every step.
type EvidenceImportArgs struct {
	Op     string `json:"op"`
	CaseID string `json:"caseId"` // the vault file is <caseId>.mp4
	Offset int64  `json:"offset,omitempty"`
	Data   []byte `json:"data,omitempty"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// EvidenceImportResult answers a step. Done: the vault already holds this
// exact file (a write at offset 0 or a commit), nothing more to send.
type EvidenceImportResult struct {
	Done bool         `json:"done"`
	File EvidenceFile `json:"file"` // set when Done
}
