package fetch

import "net"

func AllowLoopback(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	if ip.IsLoopback() {
		return true
	}
	return PublicOnly(ip)
}
