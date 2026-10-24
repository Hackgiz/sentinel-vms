package link

import "testing"

// WAVE24 D1: nil pre/post-roll = server default, a number (0 too) literal.
func TestEffectiveRollNullable(t *testing.T) {
	d := RecordingDefaults{}.Normalized()
	e := CameraConfig{}.Effective(d)
	if e.PreRollSeconds != DefaultPreRollSeconds || e.PostRollSeconds != DefaultPostRollSeconds {
		t.Fatalf("nil: %+v", e)
	}
	zero, ten := 0, 10
	e = CameraConfig{PreRollSeconds: &zero, PostRollSeconds: &zero}.Effective(d)
	if e.PreRollSeconds != 0 || e.PostRollSeconds != 0 {
		t.Fatalf("0 literal: %+v", e)
	}
	e = CameraConfig{PreRollSeconds: &ten, PostRollSeconds: &ten}.Effective(d)
	if e.PreRollSeconds != 10 || e.PostRollSeconds != 10 {
		t.Fatalf("10: %+v", e)
	}
}
