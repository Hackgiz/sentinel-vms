package onvif

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"

	"sentinel-linux/internal/onvif/fakecam"
)

func ptzCam() *fakecam.Camera {
	c := newCam()
	c.PTZ = true
	return c
}

func TestPTZSupportedProbe(t *testing.T) {
	ctx := context.Background()
	with := httptest.NewServer(ptzCam())
	defer with.Close()
	without := httptest.NewServer(newCam())
	defer without.Close()

	if ok, err := PTZSupported(ctx, with.URL+"/onvif/device_service", "admin", "s3cret&<pw"); err != nil || !ok {
		t.Fatalf("PTZ camera: ok=%v err=%v", ok, err)
	}
	if ok, err := PTZSupported(ctx, without.URL+"/onvif/device_service", "admin", "s3cret&<pw"); err != nil || ok {
		t.Fatalf("fixed camera: ok=%v err=%v", ok, err)
	}
	if _, err := PTZSupported(ctx, with.URL+"/onvif/device_service", "admin", "wrong"); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong password: err=%v, want ErrAuth", err)
	}
	if _, err := OpenPTZ(ctx, without.URL+"/onvif/device_service", "admin", "s3cret&<pw"); !errors.Is(err, ErrNoPTZ) {
		t.Fatalf("OpenPTZ on fixed camera: err=%v, want ErrNoPTZ", err)
	}
}

func TestPTZCommands(t *testing.T) {
	ctx := context.Background()
	cam := ptzCam()
	cam.SeedPreset("1", "Gate & <door>")
	srv := httptest.NewServer(cam)
	defer srv.Close()

	p, err := OpenPTZ(ctx, srv.URL+"/onvif/device_service", "admin", "s3cret&<pw")
	if err != nil {
		t.Fatal(err)
	}
	if p.profile != "main" { // the profile with a PTZConfiguration, not the first one
		t.Fatalf("profile = %q, want main", p.profile)
	}
	presets, err := p.GetPresets(ctx)
	if err != nil || len(presets) != 1 || presets[0] != (Preset{Token: "1", Name: "Gate & <door>"}) {
		t.Fatalf("presets = %+v err=%v", presets, err)
	}
	if err := p.ContinuousMove(ctx, 0.5, -2, 0); err != nil { // -2 clamps to -1
		t.Fatal(err)
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	np, err := p.SetPreset(ctx, "Back <lot>")
	if err != nil || np.Token != "2" || np.Name != "Back <lot>" {
		t.Fatalf("SetPreset = %+v err=%v", np, err)
	}
	if err := p.GotoPreset(ctx, np.Token); err != nil {
		t.Fatal(err)
	}
	if err := p.RemovePreset(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"move 0.500 -1.000 0.000", "stop", "set Back <lot>", "goto 2", "remove 1"}
	if got := cam.PTZLog(); !reflect.DeepEqual(got, want) {
		t.Fatalf("camera saw %q, want %q", got, want)
	}
	if got := cam.PTZPresets(); len(got) != 1 || got[0][0] != "2" {
		t.Fatalf("presets left = %v", got)
	}
}

func TestClampVelocity(t *testing.T) {
	for in, want := range map[float64]float64{0.3: 0.3, 5: 1, -5: -1, 0: 0} {
		if got := ClampVelocity(in); got != want {
			t.Errorf("ClampVelocity(%v) = %v, want %v", in, got, want)
		}
	}
	nan := 0.0
	nan = nan / nan
	if ClampVelocity(nan) != 0 {
		t.Error("NaN should clamp to 0")
	}
}
