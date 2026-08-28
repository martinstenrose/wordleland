package auth

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP resolves the address to rate-limit against.
//
// A reverse proxy sits in front of the application, so every request arrives
// from its address: limiting on the direct peer alone would throttle the whole
// group as though it were one client. X-Forwarded-For carries the real
// address, but a client can send that header itself, so it is believed only
// when the direct peer is a proxy we configured.
//
// The rightmost entry that is not itself trusted is the one to take. A client
// may prepend anything it likes to the header, but each trusted proxy appends
// the address it actually saw, so scanning from the right walks back through
// hops we control and stops at the first value a stranger could have written.
// Taking the leftmost entry instead — the common mistake — reads exactly the
// part an attacker controls, letting them spread a brute-force attempt across
// as many fake addresses as they care to invent.
func ClientIP(r *http.Request, trusted []*net.IPNet) string {
	peer := peerIP(r.RemoteAddr)

	// No configured proxies, or a request straight from the internet: the
	// only address we can vouch for is the one the connection came from.
	if len(trusted) == 0 || !isTrusted(peer, trusted) {
		return peer
	}

	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded == "" {
		return peer
	}

	parts := strings.Split(forwarded, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(parts[i])
		ip := net.ParseIP(candidate)
		if ip == nil {
			// Unparseable entries are treated as untrusted rather than
			// skipped: a proxy would not have written one, so this is the
			// point where the chain stops being ours.
			return peer
		}
		if !isTrusted(ip.String(), trusted) {
			return ip.String()
		}
	}

	// Every entry was a trusted proxy, which means no client address was ever
	// recorded. The peer is the closest true thing available.
	return peer
}

// peerIP strips the port from RemoteAddr, tolerating an address without one.
func peerIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func isTrusted(addr string, trusted []*net.IPNet) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	for _, network := range trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
