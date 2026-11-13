package config

import "strings"

// Intercept is the transparent part of a listener: connections a firewall
// sent to the proxy, which carry no CONNECT.
type Intercept struct {
	// Issuer signs the certificates shown for each site.
	Issuer *TLS
	// Integration is the ID of every call on the port; empty means
	// not-passed.
	Integration string
	// Passthrough lists sites relayed untouched.
	Passthrough Passthrough
	// ECHRelay lists the ECH outer names relayed untouched.
	ECHRelay []string
}

// Capture is the capture part of a listener, the ingress of the macOS
// capture extension.
type Capture struct {
	// Issuer signs the certificates shown for each site.
	Issuer *TLS
	// Passthrough lists sites relayed untouched.
	Passthrough Passthrough
	// ports are the ports, written as text, whose connections are read as
	// calls. Every other port is relayed untouched (Peter, 2026-11-13).
	ports map[string]bool
}

// HTTP says whether a captured connection to port is read as calls: TLS,
// or else plain HTTP. Every other port is relayed untouched.
func (c *Capture) HTTP(port string) bool { return c.ports[port] }

// Passes says whether host is relayed untouched: it is listed in
// passthrough, exactly or by a "*." suffix.
func (i *Intercept) Passes(host string) bool {
	return i.Passthrough.Passes(host)
}

// Passthrough lists sites relayed untouched, each a lower case host name or
// "*." and a suffix.
type Passthrough []string

// Passes says whether host is listed, exactly or by a "*." suffix.
func (pt Passthrough) Passes(host string) bool {
	host = strings.ToLower(host)
	for _, p := range pt {
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
