package auth

import (
	"net"
	"net/http"
	"testing"
)

func mustCIDRs(t *testing.T, entries ...string) []*net.IPNet {
	t.Helper()
	var nets []*net.IPNet
	for _, e := range entries {
		_, n, err := net.ParseCIDR(e)
		if err != nil {
			t.Fatalf("parse %q: %v", e, err)
		}
		nets = append(nets, n)
	}
	return nets
}

func TestClientIP(t *testing.T) {
	proxies := mustCIDRs(t, "10.0.0.0/8", "172.20.0.0/16")

	tests := []struct {
		name       string
		remoteAddr string
		forwarded  string
		trusted    []*net.IPNet
		want       string
	}{
		{
			name:       "no proxies configured",
			remoteAddr: "203.0.113.7:54321",
			trusted:    nil,
			want:       "203.0.113.7",
		},
		{
			// The header is ignored entirely: a client sending it directly
			// gets no say in which address it is limited against.
			name:       "untrusted peer sending the header",
			remoteAddr: "203.0.113.7:54321",
			forwarded:  "198.51.100.9",
			trusted:    proxies,
			want:       "203.0.113.7",
		},
		{
			name:       "trusted proxy, single entry",
			remoteAddr: "10.0.0.5:41234",
			forwarded:  "203.0.113.7",
			trusted:    proxies,
			want:       "203.0.113.7",
		},
		{
			name:       "trusted proxy, no header",
			remoteAddr: "10.0.0.5:41234",
			trusted:    proxies,
			want:       "10.0.0.5",
		},
		{
			// A proxy on a dual-stack network is two ranges, and both have
			// to be listed: a v6 peer matched against a v4 range only
			// would fall through to being treated as an untrusted client.
			// 2001:db8::/32 is the documentation prefix.
			name:       "IPv6 proxy, listed alongside the IPv4 range",
			remoteAddr: "[2001:db8:1::5]:41234",
			forwarded:  "203.0.113.7",
			trusted:    mustCIDRs(t, "172.18.0.0/16", "2001:db8:1::/64"),
			want:       "203.0.113.7",
		},
		{
			name:       "IPv4 proxy from the same pair",
			remoteAddr: "172.18.0.7:41234",
			forwarded:  "203.0.113.7",
			trusted:    mustCIDRs(t, "172.18.0.0/16", "2001:db8:1::/64"),
			want:       "203.0.113.7",
		},
		{
			// Outside both: the header is not believed at all.
			name:       "a peer in neither range",
			remoteAddr: "[2001:db8:9::5]:41234",
			forwarded:  "203.0.113.7",
			trusted:    mustCIDRs(t, "172.18.0.0/16", "2001:db8:1::/64"),
			want:       "2001:db8:9::5",
		},
		{
			// A catch-all range is not the shortcut it looks like. With
			// everything trusted there is no untrusted entry to stop at,
			// so the walk runs off the end and falls back to the peer —
			// which is the proxy. Every client in the world then shares
			// one rate-limit key, and ten failed logins from anybody lock
			// out everybody. Trust the proxy's own range, not all of them.
			name:       "a catch-all range collapses every client to the proxy",
			remoteAddr: "10.0.0.5:41234",
			forwarded:  "203.0.113.7",
			trusted:    mustCIDRs(t, "0.0.0.0/0"),
			want:       "10.0.0.5",
		},
		{
			// The attack this function exists to defeat: everything left of
			// the rightmost untrusted entry is attacker-supplied. Taking the
			// leftmost value would let one client claim a fresh address per
			// request and sidestep the login limiter entirely.
			name:       "spoofed prefix is ignored",
			remoteAddr: "10.0.0.5:41234",
			forwarded:  "1.2.3.4, 5.6.7.8, 203.0.113.7",
			trusted:    proxies,
			want:       "203.0.113.7",
		},
		{
			name:       "chained trusted proxies",
			remoteAddr: "10.0.0.5:41234",
			forwarded:  "203.0.113.7, 172.20.0.3, 10.0.0.9",
			trusted:    proxies,
			want:       "203.0.113.7",
		},
		{
			name:       "spoofed prefix behind chained proxies",
			remoteAddr: "10.0.0.5:41234",
			forwarded:  "9.9.9.9, 203.0.113.7, 172.20.0.3",
			trusted:    proxies,
			want:       "203.0.113.7",
		},
		{
			name:       "surrounding whitespace",
			remoteAddr: "10.0.0.5:41234",
			forwarded:  "  203.0.113.7 , 10.0.0.9 ",
			trusted:    proxies,
			want:       "203.0.113.7",
		},
		{
			// A proxy would not write an unparseable entry, so this is where
			// the chain stops being ours.
			name:       "garbage in the header",
			remoteAddr: "10.0.0.5:41234",
			forwarded:  "not-an-ip",
			trusted:    proxies,
			want:       "10.0.0.5",
		},
		{
			name:       "every entry is a trusted proxy",
			remoteAddr: "10.0.0.5:41234",
			forwarded:  "10.0.0.9, 172.20.0.3",
			trusted:    proxies,
			want:       "10.0.0.5",
		},
		{
			name:       "IPv6 client",
			remoteAddr: "10.0.0.5:41234",
			forwarded:  "2001:db8::1",
			trusted:    proxies,
			want:       "2001:db8::1",
		},
		{
			name:       "IPv6 peer without a port",
			remoteAddr: "2001:db8::5",
			trusted:    nil,
			want:       "2001:db8::5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "/", nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.RemoteAddr = tt.remoteAddr
			if tt.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tt.forwarded)
			}

			if got := ClientIP(req, tt.trusted); got != tt.want {
				t.Errorf("ClientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
