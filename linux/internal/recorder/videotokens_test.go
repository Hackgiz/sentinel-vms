package recorder

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/videotoken"
)

func TestVideoTokensUseTheSnapshotKeys(t *testing.T) {
	r, _ := newTestRecorder(t)
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	signer := videotoken.NewSigner(key)
	tok, _, err := signer.Sign(videotoken.Claims{Camera: "C", Subject: "u", Rights: []videotoken.Right{videotoken.Live}, Audience: r.ServerID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// No snapshot yet: nothing verifies.
	if _, err := r.VideoTokens().Verify(tok); !errors.Is(err, videotoken.ErrBadSignature) {
		t.Fatalf("before a snapshot: %v", err)
	}
	snap := link.Snapshot{Rev: 1, VideoTokenKeys: []string{"not a key", videotoken.EncodePublicKey(signer.PublicKey())}}
	if _, err := r.ApplyConfig(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	c, err := r.VideoTokens().Verify(tok)
	if err != nil || c.Camera != "C" {
		t.Fatalf("after the snapshot: %+v %v", c, err)
	}
	// A token for another recorder is refused.
	other, _, _ := signer.Sign(videotoken.Claims{Camera: "C", Subject: "u", Rights: []videotoken.Right{videotoken.Live}, Audience: "rs-2"}, time.Minute)
	if _, err := r.VideoTokens().Verify(other); !errors.Is(err, videotoken.ErrWrongAudience) {
		t.Errorf("other audience: %v", err)
	}
	// The key is dropped from the next snapshot: its tokens stop working.
	if _, err := r.ApplyConfig(context.Background(), link.Snapshot{Rev: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.VideoTokens().Verify(tok); !errors.Is(err, videotoken.ErrBadSignature) {
		t.Errorf("after the key was removed: %v", err)
	}
}
