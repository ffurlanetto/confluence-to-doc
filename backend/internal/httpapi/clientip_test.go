package httpapi

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}
	cases := []struct {
		name, remote, xff string
		trusted           []netip.Prefix
		want              string
	}{
		{"direct client", "203.0.113.7:5555", "", trusted, "203.0.113.7"},
		{"untrusted peer cannot spoof", "203.0.113.7:5555", "198.51.100.1", trusted, "203.0.113.7"},
		{"nobody trusted by default", "10.0.0.2:5555", "198.51.100.1", nil, "10.0.0.2"},
		{"through a trusted proxy", "10.0.0.2:5555", "198.51.100.1", trusted, "198.51.100.1"},
		{"through two trusted proxies", "10.0.0.2:5555", "198.51.100.1, 10.1.1.1", trusted, "198.51.100.1"},
		{"client-written hops ignored", "10.0.0.2:5555", "6.6.6.6, 198.51.100.1, 10.1.1.1", trusted, "198.51.100.1"},
		{"garbage stops the walk", "10.0.0.2:5555", "198.51.100.1, junk", trusted, "10.0.0.2"},
		{"trusted proxy without header", "10.0.0.2:5555", "", trusted, "10.0.0.2"},
		{"IPv6", "[fd00::1]:5555", "2001:db8::5", trusted, "2001:db8::5"},
		{"IPv4-mapped peer", "[::ffff:10.0.0.2]:5555", "198.51.100.1", trusted, "198.51.100.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remote
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := clientIP(r, tc.trusted); got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}
