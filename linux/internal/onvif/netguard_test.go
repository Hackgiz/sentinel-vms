package onvif_test

import (
	"context"
	"strings"
	"testing"

	"sentinel-linux/internal/onvif"
	"sentinel-linux/internal/onvif/netguard"
)

// Backchannel tests talk to fakecam on loopback only.
func init() { netguard.LoopbackOnly() }

func TestNetguardRefusesLANAddresses(t *testing.T) {
	_, err := onvif.DialRTSP(context.Background(), "rtsp://10.0.0.5/s", "", "")
	if err == nil || !strings.Contains(err.Error(), "netguard") {
		t.Fatalf("dial to a LAN address was not refused: %v", err)
	}
}

func TestNetguardRefusesLANSOAP(t *testing.T) {
	_, err := onvif.EdgeRecordingSupported(context.Background(), "http://10.0.0.9/onvif/device_service", "admin", "pw")
	if err == nil || !strings.Contains(err.Error(), "netguard") {
		t.Fatalf("SOAP call to a LAN address was not refused: %v", err)
	}
}
