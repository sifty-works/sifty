package fetch

import "net"

// AllowLoopback allows loopback and public addresses but refuses other
// private addresses. Used in tests that run httptest servers on 127.0.0.1.
func AllowLoopback(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	if ip.IsLoopback() {
		return true
	}
	return PublicOnly(ip)
}
