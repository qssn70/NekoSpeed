package webui

import (
	"net/url"
	"testing"
)

func TestBrowserURL(t *testing.T) {
	tests := []struct {
		listenAddr string
		want       string
	}{
		{":18189", "http://localhost:18189"},
		{"[::]:18189", "http://localhost:18189"},
		{"0.0.0.0:18189", "http://localhost:18189"},
		{"localhost:18189", "http://localhost:18189"},
		{"127.0.0.1:18189", "http://127.0.0.1:18189"},
		{"[::1]:18189", "http://[::1]:18189"},
		{"192.168.1.10:18189", "http://192.168.1.10:18189"},
		{"[2001:db8::1]:8080", "http://[2001:db8::1]:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.listenAddr, func(t *testing.T) {
			got, err := browserURL(tt.listenAddr)
			if err != nil {
				t.Fatalf("browserURL(%q): %v", tt.listenAddr, err)
			}
			if got != tt.want {
				t.Errorf("browserURL(%q) = %q, want %q", tt.listenAddr, got, tt.want)
			}
			if _, err := url.ParseRequestURI(got); err != nil {
				t.Errorf("invalid browser URL %q: %v", got, err)
			}
		})
	}
}

func TestBrowserURLInvalidAddress(t *testing.T) {
	if _, err := browserURL("not-an-address"); err == nil {
		t.Fatal("expected error for invalid listening address")
	}
}
