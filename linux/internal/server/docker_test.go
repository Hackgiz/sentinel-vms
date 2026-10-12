package server

import (
	"net/http/httptest"
	"testing"
)

// In Docker's bridge network the container's own IPs are useless to a phone;
// SENTINEL_LAN_URL puts the host's address first in the pairing choices.
func TestLanURLsHonorsSentinelLanURL(t *testing.T) {
	s := newCompanionServer(t)
	t.Setenv("SENTINEL_LAN_URL", " http://192.168.1.10:8090/ ")
	urls := s.lanURLs(httptest.NewRequest("GET", "/", nil))
	if len(urls) == 0 || urls[0] != "http://192.168.1.10:8090" {
		t.Fatalf("urls = %v, want http://192.168.1.10:8090 first", urls)
	}
}
