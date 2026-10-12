package link

import "testing"

func TestAuditAndLogRoundTrip(t *testing.T) {
	e := AuditEntry{ID: "A1", Time: 1760000000, User: "System", Area: "Evidence", Action: "Wrote evidence file", Detail: "case-1.mp4 \"Gate\"\x1f"}
	e.ChainHash = e.Hash()
	roundTrip(t, Event{ID: e.ID, Type: EventAudit, ServerID: "rs-1", Time: 1760000000000, Audit: &e})
	roundTrip(t, LogArgs{Lines: 300})
	roundTrip(t, LogResult{Text: "a\nb"})

	next := AuditEntry{ID: "A2", Time: 1760000001, User: "System", Area: "Events", Action: "Deleted expired events", PreviousHash: e.ChainHash}
	if h := next.Hash(); h == e.ChainHash || len(h) != 64 {
		t.Fatalf("hash %q", h)
	}
	edited := e
	edited.Detail = "case-2.mp4"
	if edited.Hash() == e.ChainHash {
		t.Fatal("hash ignores the detail")
	}
}
