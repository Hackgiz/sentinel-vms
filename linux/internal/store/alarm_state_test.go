package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The snooze timer must not reopen an alarm an operator closed meanwhile.
func TestSetAlarmStateFromRequiresTheExpectedState(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	a, _, err := s.RaiseAlarm(ctx, AlarmInput{CameraID: "cam", Kind: "Person", Title: "Person", Severity: "Warning", At: time.Now(), Origin: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAlarmState(ctx, a.ID, "Snoozed", "op", "Snoozed for 15 min"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAlarmState(ctx, a.ID, "Resolved", "op", "Resolved"); err != nil {
		t.Fatal(err)
	}
	_, err = s.SetAlarmStateFrom(ctx, a.ID, "Snoozed", "New", "", "Snooze ended")
	if !errors.Is(err, ErrStateChanged) {
		t.Fatalf("expected ErrStateChanged, got %v", err)
	}
	got, err := s.Alarm(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "Resolved" {
		t.Fatalf("state = %q, want Resolved", got.State)
	}
	for _, line := range got.ResponseLog {
		if strings.Contains(line, "Snooze ended") {
			t.Fatalf("response log = %v, must not contain the timer's line", got.ResponseLog)
		}
	}
}
