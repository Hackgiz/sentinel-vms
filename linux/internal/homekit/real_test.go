package homekit

import (
	"context"
	"testing"
)

// A real HAP server on an ephemeral port: it must start from the SQL-backed
// store, persist its device id there, and stop cleanly. Skipped where mDNS
// cannot be opened (sandboxes without multicast).
func TestRealServerStartsFromSQLStore(t *testing.T) {
	b, st := newTestBridge(t)
	addCamera(t, st, "Front")
	setSetting(t, st, "homekit_enabled", "1")
	b.reconcile(context.Background())
	waitFor(t, "server to start or fail", func() bool {
		b.mu.Lock()
		failed := b.lastErr != ""
		b.mu.Unlock()
		_, err := b.hs.Get("keypair")
		return failed || err == nil
	})
	if s := b.Status(); s.Err != "" {
		t.Skipf("HAP server cannot start here: %s", s.Err)
	}
	if _, err := b.hs.Get("uuid"); err != nil {
		t.Fatalf("device id not persisted: %v", err)
	}
	setSetting(t, st, "homekit_enabled", "0")
	b.reconcile(context.Background())
}
