package onvif

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/onvif/fakecam"
)

func newCam() *fakecam.Camera {
	return &fakecam.Camera{User: "admin", Pass: "s3cret&<pw", Manufacturer: "Acme", Model: "X100",
		Streams: []fakecam.Stream{
			{Token: "jpeg", Name: "Snapshot", Encoding: "JPEG", Width: 3840, Height: 2160, FPS: 1, RTSPURL: "rtsp://10.0.0.5:554/mjpeg"},
			{Token: "main", Name: "Main", Encoding: "H264", Width: 2560, Height: 1440, FPS: 20, RTSPURL: "rtsp://10.0.0.5:554/stream1"},
			{Token: "sub", Name: "Sub", Encoding: "H264", Width: 640, Height: 360, FPS: 15, RTSPURL: "rtsp://10.0.0.5:554/stream2?a=1&b=2"},
		}}
}

func TestFetchDetailsSignsInAndListsStreams(t *testing.T) {
	cam := newCam()
	cam.ClockSkew = -3 * time.Hour // camera clock way off: must still authenticate
	srv := httptest.NewServer(cam)
	defer srv.Close()

	d, err := FetchDetails(context.Background(), srv.URL+"/onvif/device_service", "admin", "s3cret&<pw")
	if err != nil {
		t.Fatal(err)
	}
	if d.Manufacturer != "Acme" || d.Model != "X100" || len(d.Profiles) != 3 {
		t.Fatalf("unexpected details %+v", d)
	}
	main, sub := ChooseStreams(d.Profiles)
	if main == nil || main.Token != "main" {
		t.Fatalf("main = %+v, want H264 main (not the higher-res MJPEG)", main)
	}
	if sub == nil || sub.RTSPURL != "rtsp://10.0.0.5:554/stream2?a=1&b=2" {
		t.Fatalf("sub = %+v", sub)
	}
	if main.Encoding != "H264" || main.Width != 2560 || main.FPS != 20 {
		t.Fatalf("video encoder fields not parsed (audio <Encoding> leaked?): %+v", main)
	}
}

func TestFetchDetailsWrongPassword(t *testing.T) {
	srv := httptest.NewServer(newCam())
	defer srv.Close()
	_, err := FetchDetails(context.Background(), srv.URL+"/onvif/device_service", "admin", "nope")
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

func TestHTTPDigestFallback(t *testing.T) {
	cam := newCam()
	var sawDigest bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.Header.Get("Authorization")
		if !strings.HasPrefix(a, "Digest ") {
			w.Header().Set("WWW-Authenticate", `Digest realm="cam", nonce="abc,123", qop="auth", opaque="xyz"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.Contains(a, `username="admin"`) || !strings.Contains(a, `nonce="abc,123"`) || !strings.Contains(a, `opaque="xyz"`) {
			t.Errorf("bad digest header %q", a)
		}
		sawDigest = true
		cam.ServeHTTP(w, r)
	}))
	defer srv.Close()
	if _, err := FetchDetails(context.Background(), srv.URL+"/onvif/device_service", "admin", "s3cret&<pw"); err != nil {
		t.Fatal(err)
	}
	if !sawDigest {
		t.Fatal("digest retry never happened")
	}
}

func TestParseProbeMatches(t *testing.T) {
	msg := fakecam.ProbeMatch("http://192.168.1.40/onvif/device_service", "Front Door", "C200", "TP-Link")
	devs := parseProbeMatches(msg, "192.168.1.40")
	if len(devs) != 1 {
		t.Fatalf("got %d devices", len(devs))
	}
	d := devs[0]
	if d.Host != "192.168.1.40" || d.Name != "Front Door" || d.Model != "C200" || d.Manufacturer != "TP-Link" || d.Source != "onvif" {
		t.Fatalf("%+v", d)
	}
}

func TestPickXAddrPrefersResponder(t *testing.T) {
	got := pickXAddr([]string{"http://[fe80::1]/onvif/device_service", "http://172.17.0.2/onvif/device_service", "http://192.168.1.9:8000/onvif/device_service"}, "192.168.1.9")
	if got != "http://192.168.1.9:8000/onvif/device_service" {
		t.Fatal(got)
	}
}

func TestHostsInBounded(t *testing.T) {
	if n := len(hostsIn(mustNet(t, "192.168.1.0/24"))); n != 254 {
		t.Fatalf("/24 → %d hosts", n)
	}
	if n := len(hostsIn(mustNet(t, "10.0.0.0/16"))); n != maxSweepHosts {
		t.Fatalf("/16 → %d hosts, want capped", n)
	}
}

func mustNet(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAuthFaultDetectedFromSubcodeAlone(t *testing.T) {
	// Exact fault a TP-Link Tapo C-series returns for a missing/wrong login.
	tapo := `<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope"><SOAP-ENV:Body><SOAP-ENV:Fault><SOAP-ENV:Code><SOAP-ENV:Value>SOAP-ENV:Sender</SOAP-ENV:Value><SOAP-ENV:Subcode><SOAP-ENV:Value>ter:NotAuthorized</SOAP-ENV:Value></SOAP-ENV:Subcode></SOAP-ENV:Code><SOAP-ENV:Reason><SOAP-ENV:Text xml:lang="en">Something vendor-specific</SOAP-ENV:Text></SOAP-ENV:Reason></SOAP-ENV:Fault></SOAP-ENV:Body></SOAP-ENV:Envelope>`
	root := parseXML([]byte(tapo))
	if !isAuthFault(root, root.value("Text")) {
		t.Fatal("ter:NotAuthorized subcode not recognised")
	}
	other := strings.Replace(tapo, "ter:NotAuthorized", "ter:InvalidArgVal", 1)
	if r := parseXML([]byte(other)); isAuthFault(r, r.value("Text")) {
		t.Fatal("non-auth fault misread as auth failure")
	}
}
