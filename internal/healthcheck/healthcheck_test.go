package healthcheck

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbe(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	if err := Probe(ok.URL, time.Second); err != nil {
		t.Errorf("200: unexpected error %v", err)
	}

	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unavailable.Close()
	if err := Probe(unavailable.URL, time.Second); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("503: expected status error, got %v", err)
	}

	// Reserve a port, then release it so nothing is listening there.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	if err := Probe("http://"+addr+Path, time.Second); err == nil {
		t.Error("connection refused: expected error")
	}

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer slow.Close()
	defer close(release)
	start := time.Now()
	if err := Probe(slow.URL, 100*time.Millisecond); err == nil {
		t.Error("slow server: expected timeout error")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("slow server: probe took %v, timeout not enforced", d)
	}
}

func TestURL(t *testing.T) {
	if got, want := URL(18083), "http://127.0.0.1:18083/api/health"; got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
}
