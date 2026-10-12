package store

import (
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"testing"
)

func TestVideoTokenKeyCreatedOncePersistedAndSealed(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Concurrent first uses agree on one key.
	keys := make([]string, 8)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k, err := a.VideoTokenKey(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			keys[i] = string(k)
		}(i)
	}
	wg.Wait()
	for _, k := range keys[1:] {
		if k != keys[0] {
			t.Fatal("concurrent first uses made different keys")
		}
	}
	// Stored sealed, never as the raw seed.
	sealed := a.Setting(ctx, videoTokenKeySetting, "")
	seed := base64.StdEncoding.EncodeToString([]byte(keys[0][:32]))
	if sealed == "" || strings.Contains(sealed, seed) {
		t.Fatalf("stored value %q", sealed)
	}
	a.Close()

	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	k, err := b.VideoTokenKey(ctx)
	if err != nil || string(k) != keys[0] {
		t.Fatalf("reopened store: different key (%v)", err)
	}
	// A damaged value is an error, not a silent new key.
	_ = b.SetSetting(ctx, videoTokenKeySetting, sealed[:len(sealed)-4]+"AAAA")
	if _, err := b.VideoTokenKey(ctx); err == nil {
		t.Fatal("tampered key accepted")
	}
}
