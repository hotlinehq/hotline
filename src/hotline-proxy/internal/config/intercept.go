package config

import "strings"

// Intercept is listen.intercept: a port of its own for connections a
// firewall redirected to the proxy, which carry no CONNECT.
type Intercept struct {
	// Address is where the listener takes connections.
	Address string
	// Issuer signs the certificates shown for each site.
	Issuer *TLS
	// Integration is the ID of every call on the port; empty means
	// not-passed.
	Integration string
	// Passthrough lists sites relayed untouched, each a lower case host
	// name or "*." and a suffix.
	Passthrough []string
	// ECHRelay lists the ECH outer names relayed untouched.
	ECHRelay []string
}

// Passes says whether host is relayed untouched: it is listed in
// passthrough, exactly or by a "*." suffix.
func (i *Intercept) Passes(host string) bool {
	host = strings.ToLower(host)
	for _, p := range i.Passthrough {
		if suffix, wild := strings.CutPrefix(p, "*"); wild && strings.HasSuffix(host, suffix) && len(host) > len(suffix) || p == host {
			return true
		}
	}
	return false
}

// Relays says whether an ECH outer name is relayed untouched.
func (i *Intercept) Relays(outer string) bool {
	for _, name := range i.ECHRelay {
		if name == strings.ToLower(outer) {
			return true
		}
	}
	return false
}
