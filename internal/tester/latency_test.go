package tester

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"nekospeed/internal/model"
)

func TestHTTPRTTUsesSecondRequestOnSameConnection(t *testing.T) {
	var requests, dials atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method = %s, want HEAD", r.Method)
		}
		if requests.Add(1) == 1 {
			time.Sleep(100 * time.Millisecond)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	dialer := model.DialFunc(func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	})
	start := time.Now()
	rtt, err := httpRTTOnce(context.Background(), dialer, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || dials.Load() != 1 {
		t.Fatalf("requests=%d dials=%d, want two requests on one connection", requests.Load(), dials.Load())
	}
	if elapsed := time.Since(start).Milliseconds(); elapsed-int64(rtt) < 90 {
		t.Fatalf("warm-up leaked into RTT: whole probe=%dms RTT=%dms", elapsed, rtt)
	}
}

func TestHTTPRTTRejectsColdReconnect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if _, err := httpRTTOnce(context.Background(), directDialer(), server.URL); err == nil {
		t.Fatal("must not report a fresh connection as reused HTTP RTT")
	}
}

func TestHTTPSRejectsUntrustedCertificate(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	if _, _, err := httpsPingOnce(context.Background(), directDialer(), server.URL); err == nil {
		t.Fatal("HTTPS latency must still verify the server certificate")
	}
}

func TestLatencyURLSchemes(t *testing.T) {
	for _, tc := range []struct{ input, http, https string }{
		{"", "http://www.gstatic.com/generate_204", "https://www.gstatic.com/generate_204"},
		{"https://example.com:443/p?q=1", "http://example.com/p?q=1", "https://example.com/p?q=1"},
		{"http://[::1]:80/p", "http://[::1]/p", "https://[::1]/p"},
		{"https://example.com:8443/p", "http://example.com:8443/p", "https://example.com:8443/p"},
	} {
		h, s, err := latencyURLs(tc.input)
		if err != nil || h != tc.http || s != tc.https {
			t.Errorf("%q: got %q %q %v", tc.input, h, s, err)
		}
	}
	if _, _, err := latencyURLs("ftp://example.com/p"); err == nil {
		t.Fatal("non-HTTP URL accepted")
	}
}
