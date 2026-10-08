package auth

import (
	"testing"
	"time"
)

func TestLimiterLocksAfterMaxFailures(t *testing.T) {
	l := &Limiter{MaxFailures: 3, Window: time.Minute, LockFor: time.Minute, entries: map[string]*limiterEntry{}}
	for i := 0; i < 2; i++ {
		l.Fail("user:eric")
	}
	if locked, _ := l.Locked("user:eric"); locked {
		t.Fatal("locked too early")
	}
	l.Fail("user:eric")
	if locked, _ := l.Locked("ip:1.2.3.4", "user:eric"); !locked {
		t.Fatal("should lock after 3 failures")
	}
	l.Reset("user:eric")
	if locked, _ := l.Locked("user:eric"); locked {
		t.Fatal("reset should unlock")
	}
}

func TestPasswordRules(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
	h, err := HashPassword("correct-horse-9")
	if err != nil || !CheckPassword(h, "correct-horse-9") || CheckPassword(h, "wrong-horse-9") {
		t.Fatal("hash/check mismatch")
	}
}

func TestRoles(t *testing.T) {
	if Can("Operator", ManageCameras) || !Can("Admin", ManageCameras) || !Can("Supervisor", ViewAudit) || Can("Viewer", AcknowledgeAlarms) {
		t.Fatal("role matrix differs from the Mac app")
	}
}
