package proxy_test

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

var errProxyClosed = errors.New("the proxy closed the connection")

// interceptConf is the transparent listener setting of the config a scenario
// writes.
type interceptConf struct {
	address     string
	issuer      string
	integration string
	passthrough []string
	relay       []string
	// protocol, when set, is the listener's protocol.
	protocol string
	// without names a field to leave out.
	without string
}

// interceptSection is the transparent listener of the config.
func (w *world) interceptSection() string {
	c := w.intercept
	if c == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("  intercept:\n")
	if c.without != "address" {
		fmt.Fprintf(&b, "    address: %q\n", "tcp://"+c.address)
	}
	if c.protocol != "" {
		fmt.Fprintf(&b, "    protocol: %s\n", c.protocol)
	}
	b.WriteString("    transparent:\n")
	if c.without != "issuer" {
		fmt.Fprintf(&b, "      cert: %s\n", c.issuer)
	}
	if c.integration != "" {
		fmt.Fprintf(&b, "      integration: %s\n", c.integration)
	}
	if len(c.passthrough) > 0 {
		fmt.Fprintf(&b, "      passthrough: [%s]\n", yamlList(c.passthrough))
	}
	if len(c.relay) > 0 {
		fmt.Fprintf(&b, "      ech: { relay: [%s] }\n", yamlList(c.relay))
	}
	return b.String()
}

// yamlList writes names as the items of a YAML list.
func yamlList(names []string) string {
	items := make([]string, len(names))
	for i, name := range names {
		items[i] = fmt.Sprintf("%q", name)
	}
	return strings.Join(items, ", ")
}

func interceptSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^an issuer named customer in certs$`, func() error {
		w.useIssuer(newIssuer())
		return nil
	})
	sc.Step(`^the transparent listener picks it(?: with integration "([^"]*)")?$`, func(id string) error {
		w.intercept = &interceptConf{issuer: "customer", integration: id}
		return nil
	})
	sc.Step(`^the connect part picks the issuer named customer$`, func() error {
		w.listenConnect = "  connect: { cert: customer }\n"
		return nil
	})
	sc.Step(`^a second issuer named other, which the transparent listener picks$`, func() error {
		second := newIssuer()
		w.second = second
		w.writeFile("ca/other-cert.pem", issuerCertPEM(second))
		w.writeFile("ca/other-key.pem", issuerKeyPEM(second))
		w.issuersSection += "  other: { cert: ./ca/other-cert.pem, key: ./ca/other-key.pem }\n"
		w.intercept = &interceptConf{issuer: "other"}
		w.tunnelRoots.AddCert(second.cert)
		return nil
	})
	sc.Step(`^the transparent listener lists (\S+) in (passthrough|ech.relay)$`, func(name, list string) error {
		if list == "passthrough" {
			w.intercept.passthrough = append(w.intercept.passthrough, name)
		} else {
			w.intercept.relay = append(w.intercept.relay, name)
		}
		return nil
	})
	sc.Step(`^the original destination of the connection is the address of (\S+)$`, func(host string) error {
		w.original = netip.AddrPortFrom(w.vendor(host).ip, 443)
		w.originalKnown = true
		return nil
	})
	sc.Step(`^the original destination of the connection is (\S+)$`, func(address string) error {
		w.original, w.originalKnown = netip.MustParseAddrPort(address), true
		return nil
	})
	sc.Step(`^the original destination cannot be read$`, func() error {
		w.originalKnown = false
		return nil
	})

	sc.Step(`^the app (?:connects|has connected) to the listener naming (\S+) in the handshake$`, func(host string) error {
		return w.interceptConnect(host, "")
	})
	sc.Step(`^the app retries without ECH naming (\S+)$`, func(host string) error {
		return w.interceptConnect(host, "")
	})
	sc.Step(`^the app connects to the listener with no name in the handshake, calling the address of (\S+)$`, func(host string) error {
		return w.interceptConnect(w.vendor(host).ip.String(), "")
	})
	sc.Step(`^the app connects to the listener with no name in the handshake, calling (\S+)$`, func(ip string) error {
		return w.interceptConnect(ip, "")
	})
	sc.Step(`^the app connects to the listener with ECH and the outer name (\S+)$`, func(outer string) error {
		w.vendor(outer)
		return w.interceptConnect("api.talon.one", outer)
	})
	sc.Step(`^the app opens two connections to the listener naming (\S+)$`, func(host string) error {
		for range 2 {
			if err := w.interceptConnect(host, ""); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the app opens a tunnel to (\S+):(\d+) with CONNECT$`, func(host, port string) error {
		return w.openTunnel(host, port, "")
	})
	sc.Step(`^the app sends its call$`, func() error {
		return w.callInTunnel("Host: "+w.interceptHost, "X-Shop: shop-a")
	})
	sc.Step(`^the app sends its call with the header "([^"]+)"$`, func(header string) error {
		return w.callInTunnel(header, "X-Shop: shop-a")
	})
	sc.Step(`^the app sends its call with Proxy-Authorization for (\S+)$`, func(id string) error {
		return w.callInTunnel("Host: "+w.interceptHost, basic(id+":x"))
	})
	sc.Step(`^the app sees a certificate for the IP (\S+) signed by the interception issuer$`, func(host string) error {
		if w.tunnelErr != nil {
			return failf("TLS with the listener failed: %v", w.tunnelErr)
		}
		leaf := w.tunnelCerts[len(w.tunnelCerts)-1]
		ip := netip.MustParseAddr(w.vendor(host).ip.String())
		if !slices.ContainsFunc(leaf.IPAddresses, func(a net.IP) bool { got, _ := netip.AddrFromSlice(a); return got.Unmap() == ip }) {
			return failf("the certificate names the IPs %v, want %s", leaf.IPAddresses, ip)
		}
		return leaf.CheckSignatureFrom(w.interception.cert)
	})
	sc.Step(`^the connection is closed with no TLS set up$`, func() error {
		if w.tunnelErr == nil {
			return failf("TLS with the listener was set up")
		}
		return w.waitForRecords(1)
	})
	sc.Step(`^the app's TLS setup fails because the proxy answered as the outer name (\S+) and offered no ECH keys$`, func(outer string) error {
		var rejected *tls.ECHRejectionError
		if !errors.As(w.tunnelErr, &rejected) {
			return failf("TLS failed with %v, want the app to see ECH rejected", w.tunnelErr)
		}
		if len(rejected.RetryConfigList) != 0 {
			return failf("the proxy offered ECH keys")
		}
		if err := w.waitForRecords(1); err != nil {
			return err
		}
		return nil
	})
	sc.Step(`^the proxy dialed (\S+) by its name, never the address the app chose$`, func(host string) error {
		if !w.network.triedToDial(w.vendor(host).ip.String()) {
			return failf("the proxy did not connect to %s", host)
		}
		if w.originalKnown && w.network.triedToDial(w.original.Addr().String()) {
			return failf("the proxy connected to the address the app chose, %s", w.original.Addr())
		}
		return nil
	})
	sc.Step(`^the proxy connected to the address of (\S+)$`, func(host string) error {
		if !w.network.triedToDial(w.vendor(host).ip.String()) {
			return failf("the proxy did not connect to %s", host)
		}
		return nil
	})
	sc.Step(`^the vendor's own certificate reaches the app, not one from the interception issuer$`, func() error {
		if w.tunnelErr != nil {
			return failf("TLS with the vendor failed: %v", w.tunnelErr)
		}
		leaf := w.tunnelCerts[len(w.tunnelCerts)-1]
		if err := leaf.CheckSignatureFrom(w.issuer.cert); err != nil {
			return failf("the certificate is not the vendor's: %v", err)
		}
		return nil
	})
	sc.Step(`^the app closes its connections$`, func() error {
		for _, c := range w.tunnels {
			_ = c.Close()
		}
		return nil
	})
	sc.Step(`^the name (\S+) has no address$`, func(host string) error {
		w.vendor(host)
		w.network.forget(host)
		return nil
	})
	sc.Step(`^the app sends (.+) to the listener$`, func(kind string) error {
		if err := w.start(); err != nil {
			return err
		}
		conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", w.interceptAddr)
		if err != nil {
			return err
		}
		w.tunnels = append(w.tunnels, conn)
		if _, err := conn.Write(oddHello(kind)); err != nil {
			return err
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.Copy(io.Discard, conn)
		w.tunnelErr = errProxyClosed
		return nil
	})
	sc.Step(`^the config file drops the transparent listener$`, func() error {
		w.intercept = nil
		w.writeFile("hotline-proxy.yaml", w.config())
		return nil
	})
	sc.Step(`^the app's TLS setup fails$`, func() error {
		if w.tunnelErr == nil {
			return failf("TLS with the listener was set up")
		}
		return nil
	})
	sc.Step(`^the (?:intercept|capture) record has (.+)$`, w.interceptRecordWith)

	sc.Step(`^another program listens on the transparent listener address$`, func() error {
		l := listenLocal()
		w.listeners = append(w.listeners, l)
		w.useIssuer(newIssuer())
		w.intercept = &interceptConf{address: l.Addr().String(), issuer: "customer"}
		w.busy = w.intercept.address
		return nil
	})
	sc.Step(`^the transparent listener is added$`, func() error {
		w.useIssuer(newIssuer())
		w.intercept = &interceptConf{address: freePort(), issuer: "customer"}
		return nil
	})
	sc.Step(`^the transparent listener lists an ech.relay name that is not a host name$`, func() error {
		w.problem = `ech.relay "a/b" must be a host name`
		w.useIssuer(newIssuer())
		w.intercept = &interceptConf{address: freePort(), issuer: "customer", relay: []string{"a/b"}}
		return nil
	})
	sc.Step(`^the transparent listener names a cert that is not in certs$`, func() error {
		w.problem = "listener intercept: transparent names the cert gone, which is not in certs"
		w.useIssuer(newIssuer())
		w.intercept = &interceptConf{address: freePort(), issuer: "gone"}
		return nil
	})
	sc.Step(`^the transparent listener names no cert$`, func() error {
		w.problem = "listener intercept: transparent needs a cert"
		w.useIssuer(newIssuer())
		w.intercept = &interceptConf{address: freePort(), issuer: "customer", without: "issuer"}
		return nil
	})
	sc.Step(`^the transparent listener has no address$`, func() error {
		w.problem = "listener intercept needs an address"
		w.useIssuer(newIssuer())
		w.intercept = &interceptConf{issuer: "customer", without: "address"}
		return nil
	})
	sc.Step(`^the transparent listener lists a passthrough pattern that is not a name or a \*\. suffix$`, func() error {
		w.problem = `passthrough "api.*.com" must be a host name or "*." and a suffix`
		w.useIssuer(newIssuer())
		w.intercept = &interceptConf{address: freePort(), issuer: "customer", passthrough: []string{"api.*.com"}}
		return nil
	})
}

// interceptRecordWith checks that the last record is a minimal record of a
// connection that never became a call, with the fields listed as
// "key value", separated by commas and "and".
func (w *world) interceptRecordWith(fields string) error {
	if err := w.waitForRecords(1); err != nil {
		return err
	}
	record, err := w.lastRecord()
	if err != nil {
		return err
	}
	allowed := map[string]bool{
		"hotline.integration.id": true, "hotline.ingress": true, "client.address": true, "server.address": true,
		"error.type": true, "hotline.timing.total_us": true, "hotline.intercept.mode": true, "hotline.intercept.reason": true,
		"process.pid": true, "process.executable.path": true,
	}
	for key := range record {
		if !allowed[key] {
			return failf("the record has %s, which a connection that never became a call has no use for", key)
		}
	}
	for _, field := range strings.FieldsFunc(strings.ReplaceAll(fields, " and ", ","), func(r rune) bool { return r == ',' }) {
		key, value, _ := strings.Cut(strings.TrimSpace(field), " ")
		if err := has(record, key, value); err != nil {
			return err
		}
	}
	return hasAll(record, "hotline.integration.id", "hotline.ingress", "client.address", "hotline.timing.total_us")
}

// interceptConnect opens a connection to the intercept listener and sets
// up TLS, naming host in the handshake: no name when host is an IP address,
// which Go's client does not send as a name. With outer set, the app uses
// ECH with that outer name, as an app does that found an ECH config for
// the real host. The result is kept like a tunnel's: w.tunnel, w.tunnelCerts
// and w.tunnelErr.
func (w *world) interceptConnect(host, outer string) error {
	if err := w.start(); err != nil {
		return err
	}
	if w.interceptAddr == "" {
		return failf("the scenario has no the transparent listener")
	}
	if net.ParseIP(host) == nil {
		w.vendor(host)
	}
	w.interceptHost = host
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(context.Background(), "tcp", w.interceptAddr)
	if err != nil {
		return err
	}
	w.tunnels = append(w.tunnels, conn)
	roots := w.tunnelRoots.Clone()
	// A relayed connection shows the vendor's own certificate.
	roots.AddCert(w.issuer.cert)
	config := &tls.Config{RootCAs: roots, ServerName: host, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12}
	if outer != "" {
		config.MinVersion = tls.VersionTLS13
		config.EncryptedClientHelloConfigList = echConfigList(outer)
	}
	client := tls.Client(conn, config)
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	w.tunnelErr = client.HandshakeContext(context.Background())
	_ = client.SetDeadline(time.Time{})
	if state := client.ConnectionState(); state.HandshakeComplete {
		w.tunnelCerts = append(w.tunnelCerts, state.PeerCertificates[0])
		w.tunnel = client
	}
	return nil
}

// echConfigList is an ECH config for a public name, with a key nobody holds:
// enough for Go's client to send an ECH handshake whose outer name is the
// public name.
func echConfigList(public string) []byte {
	u16 := func(b []byte, v int) []byte { return binary.BigEndian.AppendUint16(b, uint16(v)) } //nolint:gosec // short test values
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	contents := []byte{7}
	contents = u16(contents, 0x0020) // X25519
	contents = u16(contents, len(key))
	contents = append(contents, key...)
	contents = u16(contents, 4)
	contents = u16(contents, 1)                        // HKDF-SHA256
	contents = u16(contents, 1)                        // AES-128-GCM
	contents = append(contents, 64, byte(len(public))) //nolint:gosec // a short test name
	contents = append(contents, public...)
	contents = u16(contents, 0)
	config := u16(nil, 0xfe0d)
	config = u16(config, len(contents))
	config = append(config, contents...)
	return append(u16(nil, len(config)), config...)
}

// oddHello is the first bytes of a connection that is not a normal client
// hello, or one that holds no usable site name, by what is wrong with it.
func oddHello(kind string) []byte {
	be16 := func(v int) []byte { return binary.BigEndian.AppendUint16(nil, uint16(v)) } //nolint:gosec // short test values
	record := func(body []byte) []byte { return append(append([]byte{0x16, 3, 1}, be16(len(body))...), body...) }
	handshake := func(kind byte, body []byte) []byte {
		return append([]byte{kind, 0, byte(len(body) >> 8), byte(len(body))}, body...) //nolint:gosec // short test values
	}
	// A hello body up to its compression methods.
	start := append(append([]byte{3, 3}, make([]byte, 32)...), 0, 0, 2, 0, 0x2f, 1, 0)
	extension := func(kind int, body []byte) []byte { return append(append(be16(kind), be16(len(body))...), body...) }
	withExtensions := func(extensions []byte) []byte {
		body := append(append(append([]byte{}, start...), be16(len(extensions))...), extensions...)
		return record(handshake(1, body))
	}
	switch kind {
	case "plain HTTP bytes":
		return []byte("GET / HTTP/1.1\r\nHost: api.talon.one\r\n\r\n")
	case "a handshake message that is not a hello":
		return record(handshake(2, start))
	case "a record larger than TLS allows":
		return []byte{0x16, 3, 1, 0x40, 1}
	case "an empty record":
		return []byte{0x16, 3, 1, 0, 0}
	case "a record cut short":
		return append([]byte{0x16, 3, 1, 0, 100}, make([]byte, 10)...)
	case "a hello that ends after its random bytes":
		return record(handshake(1, append([]byte{3, 3}, make([]byte, 32)...)))
	case "a hello cut inside its session ID":
		return record(handshake(1, append(append([]byte{3, 3}, make([]byte, 32)...), 50, 1, 2, 3)))
	case "a hello whose extension overruns it":
		return record(handshake(1, append(append(append([]byte{}, start...), be16(8)...), 0, 0, 0, 100)))
	case "a hello with no extensions":
		return record(handshake(1, start))
	case "a hello with a site name too short to read":
		return withExtensions(extension(0, []byte{0, 1}))
	case "a hello with a site name longer than its extension":
		return withExtensions(extension(0, []byte{0, 9, 0, 0, 20, 'a'}))
	case "a hello with an extensions length that stops early":
		name := []byte("api.talon.one")
		list := append(append([]byte{0}, be16(len(name))...), name...)
		extensions := extension(0, append(be16(len(list)), list...))
		body := append(append(append([]byte{}, start...), be16(len(extensions))...), extensions...)
		return record(handshake(1, append(body, 0, 0, 0, 0)))
	}
	return nil
}
