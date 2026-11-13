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
	// Passthrough lists sites relayed untouched.
	Passthrough Passthrough
	// ECHRelay lists the ECH outer names relayed untouched.
	ECHRelay []string
}

// Capture is listen.capture, the ingress of the macOS capture extension.
type Capture struct {
	// Path is the socket file the extension sends its CONNECT to, with ~/
	// already read as the home folder.
	Path string
	// Issuer signs the certificates shown for each site.
	Issuer *TLS
	// Passthrough lists sites relayed untouched.
	Passthrough Passthrough
	// http are the ports, written as text, whose connections are read as
	// calls. Every other port is tcp: relayed untouched (Peter, 2026-11-13).
	http map[string]bool
}

// HTTP says whether a captured connection to port is read as calls: TLS,
// or else plain HTTP. Every other port is relayed untouched.
func (c *Capture) HTTP(port string) bool { return c.http[port] }

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
