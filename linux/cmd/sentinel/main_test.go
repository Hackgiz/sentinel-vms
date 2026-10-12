package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbe(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"setupRequired":true}`))
	}))
	defer ok.Close()
	_, port, _ := net.SplitHostPort(ok.Listener.Addr().String())
	if err := probe(":" + port); err != nil {
		t.Fatalf("healthy server: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer bad.Close()
	_, port, _ = net.SplitHostPort(bad.Listener.Addr().String())
	if err := probe("0.0.0.0:" + port); err == nil {
		t.Fatal("500 should be unhealthy")
	}
	if err := probe("nonsense"); err == nil {
		t.Fatal("bad -listen should be an error")
	}
}
