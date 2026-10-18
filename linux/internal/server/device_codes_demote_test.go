package server

import (
	"context"
	"testing"
	"time"
)

// The Viewer cap may lower a role, never raise one: once the approver's
// role no longer views live, the TV's tokens stop working too.
func TestDeviceCodeCapNeverRaisesADemotedRole(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	role, err := s.Store.CreateRole(ctx, "Night guard", "", []string{"viewLive", "acknowledgeAlarms"})
	if err != nil {
		t.Fatal(err)
	}
	guard := v2SignIn(t, s, "guard", "Night guard")["accessToken"].(string)

	_, g := issueDeviceCode(t, h, "10.0.0.5", "Desk TV")
	userCode, deviceCode := g["userCode"].(string), g["deviceCode"].(string)
	if code, m := v2Call(t, h, "POST", "/api/v2/device-codes/approve", guard, `{"userCode":"`+userCode+`"}`); code != 200 {
		t.Fatalf("approve: %d %v", code, m)
	}
	rewindLastPoll(t, s, 6*time.Second)
	code, tok := pollToken(t, h, deviceCode)
	if code != 200 {
		t.Fatalf("redeem: %d %v", code, tok)
	}
	access, refresh := tok["accessToken"].(string), tok["refreshToken"].(string)
	if code, me := v2Call(t, h, "GET", "/api/v2/me", access, ""); code != 200 || me["role"] != "Viewer" {
		t.Fatalf("me while the role views live: %d %v", code, me)
	}

	// The admin takes live view away from the role.
	if _, err := s.Store.UpdateRole(ctx, role.ID, role.Name, "", []string{"acknowledgeAlarms"}); err != nil {
		t.Fatal(err)
	}
	s.invalidateRoles()
	if code, _ := v2Call(t, h, "GET", "/api/v2/cameras", access, ""); code != 401 {
		t.Fatalf("TV after demotion: %d, want 401", code)
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"refresh","refreshToken":"`+refresh+`"}`); code == 200 {
		t.Fatalf("refresh after demotion succeeded: %v", m)
	}
}
