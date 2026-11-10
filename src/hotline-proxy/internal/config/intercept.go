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
func (i *Intercept) Passes(host string) bool { return passes(i.Passthrough, host) }

// Relays says whether an ECH outer name is relayed untouched.
func (i *Intercept) Relays(outer string) bool { return relays(i.ECHRelay, outer) }

// Capture is listen.capture: an ingress for the calls of chosen apps on a
// Mac, which the macOS extension hands to a socket the proxy makes itself.
type Capture struct {
	// Issuer signs the certificates shown for each site.
	Issuer *TLS
	// Integration is the ID of every call on the socket; empty means
	// not-passed.
	Integration string
	// Apps are the programs to capture, by name or process number; a
	// leading "!" skips the match.
	Apps []string
	// Passthrough and ECHRelay mean the same as for Intercept.
	Passthrough []string
	ECHRelay    []string
}

// Passes says whether host is relayed untouched.
func (c *Capture) Passes(host string) bool { return passes(c.Passthrough, host) }

// Relays says whether an ECH outer name is relayed untouched.
func (c *Capture) Relays(outer string) bool { return relays(c.ECHRelay, outer) }

func passes(list []string, host string) bool {
	host = strings.ToLower(host)
	for _, p := range list {
		if suffix, wild := strings.CutPrefix(p, "*"); wild && strings.HasSuffix(host, suffix) && len(host) > len(suffix) || p == host {
			return true
		}
	}
	return false
}

func relays(list []string, outer string) bool {
	for _, name := range list {
		if name == strings.ToLower(outer) {
			return true
		}
	}
	return false
}
