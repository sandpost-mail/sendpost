package ratelimit

import "net/netip"

// ipv6KeyBits is the IPv6 prefix that identifies one client. A single host
// usually controls a whole /64, so keying on the full address would let it
// rotate addresses to escape the limit.
const ipv6KeyBits = 64

// AddrKey returns the limiter key for a client address: the address itself
// for IPv4 (including IPv4-mapped IPv6), its /64 prefix for IPv6.
func AddrKey(a netip.Addr) string {
	a = a.Unmap()
	if a.Is4() || !a.IsValid() {
		return a.String()
	}
	p, err := a.WithZone("").Prefix(ipv6KeyBits)
	if err != nil {
		return a.String()
	}
	return p.String()
}

// Key is AddrKey for a textual address. Strings that are not an IP address
// (for example a Unix socket name) are returned unchanged.
func Key(addr string) string {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return addr
	}
	return AddrKey(a)
}
