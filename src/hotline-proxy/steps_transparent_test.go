package proxy_test

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	proxy "hotline-proxy"
	"io"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

var errProxyClosed = errors.New("the proxy closed the connection")

// transparentConf is the transparent listener setting of the config a scenario
// writes.
type transparentConf struct {
	address     string
	authority   string
	integration string
	passthrough []string
	relay       []string
	// protocol, when set, is the listener's protocol.
	protocol string
	// without names a field to leave out.
	without string
}

// transparentSection is the transparent listener of the config.
func (sut *transparentSUT) transparentSection() string {
	c := sut.setting
	if c == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("  transparent:\n")
	if c.without != "address" {
		fmt.Fprintf(&b, "    address: %q\n", "tcp://"+c.address)
	}
	if c.protocol != "" {
		fmt.Fprintf(&b, "    protocol: %s\n", c.protocol)
	}
	b.WriteString("    transparent:\n")
	if c.without != "cert" {
		fmt.Fprintf(&b, "      cert: %s\n", c.authority)
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

func transparentSteps(sc *godog.ScenarioContext, sut *transparentSUT) {
	sc.Step(`^a certificate authority named customer in certs$`, func() error {
		sut.certs.useAuthority(newAuthority())
		return nil
	})
	sc.Step(`^the transparent listener picks it(?: with integration "([^"]*)")?$`, func(id string) error {
		sut.setting = &transparentConf{authority: "customer", integration: id}
		return nil
	})
	sc.Step(`^the connect part picks the certificate authority named customer$`, func() error {
		sut.connect.listenConnect = "  connect: { cert: customer }\n"
		return nil
	})
	sc.Step(`^a second certificate authority named other, which the transparent listener picks$`, func() error {
		second := newAuthority()
		sut.certs.second = second
		sut.proxy.writeFile("ca/other-cert.pem", authorityCertPEM(second))
		sut.proxy.writeFile("ca/other-key.pem", authorityKeyPEM(second))
		sut.certs.certEntries += "  other: { cert: ./ca/other-cert.pem, key: ./ca/other-key.pem }\n"
		sut.setting = &transparentConf{authority: "other"}
		sut.app.tunnelRoots.AddCert(second.cert)
		return nil
	})
	sc.Step(`^the transparent listener lists (\S+) in (passthrough|ech.relay)$`, func(name, list string) error {
		if list == "passthrough" {
			sut.setting.passthrough = append(sut.setting.passthrough, name)
		} else {
			sut.setting.relay = append(sut.setting.relay, name)
		}
		return nil
	})
	sc.Step(`^the original destination of the connection is the address of (\S+)$`, func(host string) error {
		sut.original = netip.AddrPortFrom(sut.vendors.vendor(host).ip, 443)
		sut.originalKnown = true
		return nil
	})
	sc.Step(`^the original destination of the connection is (\S+)$`, func(address string) error {
		sut.original, sut.originalKnown = netip.MustParseAddrPort(address), true
		return nil
	})
	sc.Step(`^the original destination cannot be read$`, func() error {
		sut.originalKnown = false
		return nil
	})

	sc.Step(`^the app (?:connects|has connected) to the listener naming (\S+) in the handshake$`, func(host string) error {
		return sut.transparentConnect(host, "")
	})
	sc.Step(`^the app retries without ECH naming (\S+)$`, func(host string) error {
		return sut.transparentConnect(host, "")
	})
	sc.Step(`^the app connects to the listener with no name in the handshake, calling the address of (\S+)$`, func(host string) error {
		return sut.transparentConnect(sut.vendors.vendor(host).ip.String(), "")
	})
	sc.Step(`^the app connects to the listener with no name in the handshake, calling (\S+)$`, func(ip string) error {
		return sut.transparentConnect(ip, "")
	})
	sc.Step(`^the app connects to the listener with ECH and the outer name (\S+)$`, func(outer string) error {
		sut.vendors.vendor(outer)
		return sut.transparentConnect("api.talon.one", outer)
	})
	sc.Step(`^the app opens two connections to the listener naming (\S+)$`, func(host string) error {
		for range 2 {
			if err := sut.transparentConnect(host, ""); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the app opens a tunnel to (\S+):(\d+) with CONNECT$`, func(host, port string) error {
		return sut.connect.openTunnel(host, port, "")
	})
	sc.Step(`^the app sends its call$`, func() error {
		return sut.app.callInTunnel("Host: "+sut.app.transparentHost, "X-Shop: shop-a")
	})
	sc.Step(`^the app sends its call with the header "([^"]+)"$`, func(header string) error {
		return sut.app.callInTunnel(header, "X-Shop: shop-a")
	})
	sc.Step(`^the app sends its call with Proxy-Authorization for (\S+)$`, func(id string) error {
		return sut.app.callInTunnel("Host: "+sut.app.transparentHost, basic(id+":x"))
	})
	sc.Step(`^the app sees a certificate for the IP (\S+) signed by the interception certificate authority$`, func(host string) error {
		if sut.app.tunnelErr != nil {
			return failf("TLS with the listener failed: %v", sut.app.tunnelErr)
		}
		leaf := sut.app.tunnelCerts[len(sut.app.tunnelCerts)-1]
		ip := netip.MustParseAddr(sut.vendors.vendor(host).ip.String())
		if !slices.ContainsFunc(leaf.IPAddresses, func(a net.IP) bool { got, _ := netip.AddrFromSlice(a); return got.Unmap() == ip }) {
			return failf("the certificate names the IPs %v, want %s", leaf.IPAddresses, ip)
		}
		return leaf.CheckSignatureFrom(sut.certs.interception.cert)
	})
	sc.Step(`^the connection is closed with no TLS set up$`, func() error {
		if sut.app.tunnelErr == nil {
			return failf("TLS with the listener was set up")
		}
		return sut.otlp.waitForRecords(1)
	})
	sc.Step(`^the app's TLS setup fails because the proxy answered as the outer name (\S+) and offered no ECH keys$`, func(outer string) error {
		var rejected *tls.ECHRejectionError
		if !errors.As(sut.app.tunnelErr, &rejected) {
			return failf("TLS failed with %v, want the app to see ECH rejected", sut.app.tunnelErr)
		}
		if len(rejected.RetryConfigList) != 0 {
			return failf("the proxy offered ECH keys")
		}
		if err := sut.otlp.waitForRecords(1); err != nil {
			return err
		}
		return nil
	})
	sc.Step(`^the proxy dialed (\S+) by its name, never the address the app chose$`, func(host string) error {
		if !sut.vendors.network.triedToDial(sut.vendors.vendor(host).ip.String()) {
			return failf("the proxy did not connect to %s", host)
		}
		if sut.originalKnown && sut.vendors.network.triedToDial(sut.original.Addr().String()) {
			return failf("the proxy connected to the address the app chose, %s", sut.original.Addr())
		}
		return nil
	})
	sc.Step(`^the proxy connected to the address of (\S+)$`, func(host string) error {
		if !sut.vendors.network.triedToDial(sut.vendors.vendor(host).ip.String()) {
			return failf("the proxy did not connect to %s", host)
		}
		return nil
	})
	sc.Step(`^the vendor's own certificate reaches the app, not one from the interception certificate authority$`, func() error {
		if sut.app.tunnelErr != nil {
			return failf("TLS with the vendor failed: %v", sut.app.tunnelErr)
		}
		leaf := sut.app.tunnelCerts[len(sut.app.tunnelCerts)-1]
		if err := leaf.CheckSignatureFrom(sut.vendors.authority.cert); err != nil {
			return failf("the certificate is not the vendor's: %v", err)
		}
		return nil
	})
	sc.Step(`^the app closes its connections$`, func() error {
		for _, c := range sut.app.tunnels {
			_ = c.Close()
		}
		return nil
	})
	sc.Step(`^the first address of (\S+) never answers$`, func(host string) error {
		sut.proxy.clock = &fakeClock{}
		v := sut.vendors.vendor(host)
		// Time goes by one wait at a time until the proxy gives up on the
		// first address: the ledger's own waits may come first.
		sut.vendors.network.stuck = func() {
			go func() {
				for range 20 {
					if sut.vendors.network.triedToDial(v.ip.String()) {
						return
					}
					sut.proxy.clock.passToNext()
					pause(10 * time.Millisecond)
				}
			}()
		}
		dead := netip.MustParseAddr("192.0.2.77")
		sut.vendors.network.mu.Lock()
		sut.vendors.network.names[host] = []netip.Addr{dead, v.ip}
		sut.vendors.network.mu.Unlock()
		sut.vendors.network.setHang("192.0.2.77:443")
		return nil
	})
	sc.Step(`^the record's hotline.timing.total_us is at least (\d+) seconds, half of the relay's 30 second connect wait$`, func(seconds int) error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		total, err := strconv.ParseInt(fmt.Sprint(record["hotline.timing.total_us"]), 10, 64)
		if err != nil {
			return failf("the record's hotline.timing.total_us is %v", record["hotline.timing.total_us"])
		}
		if want := int64(seconds) * 1_000_000; total < want {
			return failf("the relay's record lasts %d us, less than the first address's share of %d us", total, want)
		}
		return nil
	})
	sc.Step(`^both relays' records are made and (\d+) seconds pass$`, func(seconds int) error {
		if _, err := sut.otlp.waitRecords(2); err != nil {
			return err
		}
		sut.proxy.clock.pass(time.Duration(seconds) * time.Second)
		return nil
	})
	sc.Step(`^the two relays' records are in two batches of one chain, each naming its own config$`, func() error {
		records, err := sut.otlp.waitRecords(2)
		if err != nil {
			return err
		}
		// ci-lint is not an integration of the config, so its calls go on
		// the not-passed chain.
		batches, err := sut.otlp.chain("not-passed", 2)
		if err != nil {
			return err
		}
		first, second := batchOf(batches, records[0]), batchOf(batches, records[1])
		if first == nil || second == nil {
			return failf("a relay's record is in no not-passed batch")
		}
		if first == second {
			return failf("both relays' records are in one batch, of config %v", first.attrs["hotline.batch.config"])
		}
		if first.attrs["hotline.batch.config"] == second.attrs["hotline.batch.config"] {
			return failf("both batches name the config %v", first.attrs["hotline.batch.config"])
		}
		return checkChain(batches)
	})
	sc.Step(`^the name (\S+) has no address$`, func(host string) error {
		sut.vendors.vendor(host)
		sut.vendors.network.forget(host)
		return nil
	})
	sc.Step(`^the app sends (.+) to the listener$`, func(kind string) error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", sut.transparentAddr)
		if err != nil {
			return err
		}
		sut.app.tunnels = append(sut.app.tunnels, conn)
		if _, err := conn.Write(oddHello(kind)); err != nil {
			return err
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.Copy(io.Discard, conn)
		sut.app.tunnelErr = errProxyClosed
		return nil
	})
	sc.Step(`^the config file adds (\S+) to the transparent listener's passthrough$`, func(name string) error {
		sut.setting.passthrough = append(sut.setting.passthrough, name)
		sut.proxy.writeFile("hotline-proxy.yaml", sut.proxy.configText())
		return nil
	})
	sc.Step(`^the config file drops the transparent listener$`, func() error {
		sut.setting = nil
		sut.proxy.writeFile("hotline-proxy.yaml", sut.proxy.configText())
		return nil
	})
	sc.Step(`^the app's TLS setup fails$`, func() error {
		if sut.app.tunnelErr == nil {
			return failf("TLS with the listener was set up")
		}
		return nil
	})
	sc.Step(`^the (?:transparent|capture) record has (.+)$`, sut.transparentRecordWith)

	sc.Step(`^another program listens on the transparent listener address$`, func() error {
		l := listenLocal()
		sut.proxy.opened = append(sut.proxy.opened, l)
		sut.certs.useAuthority(newAuthority())
		sut.setting = &transparentConf{address: l.Addr().String(), authority: "customer"}
		sut.proxy.busy = sut.setting.address
		return nil
	})
	sc.Step(`^the transparent listener is added$`, func() error {
		sut.certs.useAuthority(newAuthority())
		sut.setting = &transparentConf{address: sut.proxy.ports.take(), authority: "customer"}
		return nil
	})
	sc.Step(`^the transparent listener lists an ech.relay name that is not a host name$`, func() error {
		sut.proxy.problem = `ech.relay "a/b" must be a host name`
		sut.certs.useAuthority(newAuthority())
		sut.setting = &transparentConf{address: sut.proxy.ports.take(), authority: "customer", relay: []string{"a/b"}}
		return nil
	})
	sc.Step(`^the transparent listener names a cert that is not in certs$`, func() error {
		sut.proxy.problem = "listener transparent: transparent names the cert gone, which is not in certs"
		sut.certs.useAuthority(newAuthority())
		sut.setting = &transparentConf{address: sut.proxy.ports.take(), authority: "gone"}
		return nil
	})
	sc.Step(`^the transparent listener names no cert$`, func() error {
		sut.proxy.problem = "listener transparent: transparent needs a cert"
		sut.certs.useAuthority(newAuthority())
		sut.setting = &transparentConf{address: sut.proxy.ports.take(), authority: "customer", without: "cert"}
		return nil
	})
	sc.Step(`^the transparent listener has no address$`, func() error {
		sut.proxy.problem = "listener transparent needs an address"
		sut.certs.useAuthority(newAuthority())
		sut.setting = &transparentConf{authority: "customer", without: "address"}
		return nil
	})
	sc.Step(`^the transparent listener lists a passthrough pattern that is not a name or a \*\. suffix$`, func() error {
		sut.proxy.problem = `passthrough "api.*.com" must be a host name or "*." and a suffix`
		sut.certs.useAuthority(newAuthority())
		sut.setting = &transparentConf{address: sut.proxy.ports.take(), authority: "customer", passthrough: []string{"api.*.com"}}
		return nil
	})
}

// transparentRecordWith checks that the last record is a minimal record of a
// connection that never became a call, with the fields listed as
// "key value", separated by commas and "and".
func (sut *transparentSUT) transparentRecordWith(fields string) error {
	if err := sut.otlp.waitForRecords(1); err != nil {
		return err
	}
	record, err := sut.otlp.lastRecord()
	if err != nil {
		return err
	}
	allowed := map[string]bool{
		"hotline.integration.id": true, "hotline.ingress": true, "client.address": true, "server.address": true,
		"error.type": true, "hotline.timing.total_us": true, "hotline.intercept.mode": true, "hotline.intercept.reason": true,
		"process.pid": true, "process.executable.path": true,
	}
	for key := range record {
		if !allowed[key] && !ledgerField(key) {
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

// transparentConnect opens a connection to the transparent listener and sets
// up TLS, naming host in the handshake: no name when host is an IP address,
// which Go's client does not send as a name. With outer set, the app uses
// ECH with that outer name, as an app does that found an ECH config for
// the real host. The result is kept like a tunnel's: w.tunnel, w.tunnelCerts
// and w.tunnelErr.
func (sut *transparentSUT) transparentConnect(host, outer string) error {
	if err := sut.proxy.start(); err != nil {
		return err
	}
	if sut.transparentAddr == "" {
		return failf("the scenario has no the transparent listener")
	}
	if net.ParseIP(host) == nil {
		sut.vendors.vendor(host)
	}
	sut.app.transparentHost = host
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(context.Background(), "tcp", sut.transparentAddr)
	if err != nil {
		return err
	}
	sut.app.tunnels = append(sut.app.tunnels, conn)
	roots := sut.app.tunnelRoots.Clone()
	// A relayed connection shows the vendor's own certificate.
	roots.AddCert(sut.vendors.authority.cert)
	config := &tls.Config{RootCAs: roots, ServerName: host, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12}
	if outer != "" {
		config.MinVersion = tls.VersionTLS13
		config.EncryptedClientHelloConfigList = echConfigList(outer)
	}
	client := tls.Client(conn, config)
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	sut.app.tunnelErr = client.HandshakeContext(context.Background())
	_ = client.SetDeadline(time.Time{})
	if state := client.ConnectionState(); state.HandshakeComplete {
		sut.app.tunnelCerts = append(sut.app.tunnelCerts, state.PeerCertificates[0])
		sut.app.tunnel = client
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

var errNoOriginal = errors.New("the original destination is not known")

// transparentSUT is the transparent ingress: connections the network sends
// to the proxy without the app knowing.
type transparentSUT struct {
	proxy   *proxySUT
	vendors *vendorsSUT
	certs   *certsSUT
	app     *appSUT
	otlp    *otlpSUT
	connect *connectSUT
	// setting is the transparent listener, when the scenario has one, and
	// transparentAddr the address start picked for it.
	setting         *transparentConf
	transparentAddr string
	// original is what the app's connection was first sent to, when
	// originalKnown lets the proxy read it.
	original      netip.AddrPort
	originalKnown bool
}

func newTransparentSUT(root *proxySUT, vendors *vendorsSUT, certs *certsSUT, app *appSUT, otlp *otlpSUT, connect *connectSUT) *transparentSUT {
	sut := &transparentSUT{proxy: root, vendors: vendors, certs: certs, app: app, otlp: otlp, connect: connect}
	root.beforeEachStart(func() {
		if sut.setting != nil && sut.setting.address == "" && sut.setting.without != "address" {
			root.pick(&sut.setting.address)
			sut.transparentAddr = sut.setting.address
		}
	})
	root.addConfig(configIngressListeners, sut.transparentSection)
	root.addOption(func(o *proxy.Options) {
		o.OriginalDestination = func(net.Conn) (netip.AddrPort, error) {
			if !sut.originalKnown {
				return netip.AddrPort{}, errNoOriginal
			}
			return sut.original, nil
		}
	})
	return sut
}

// AddSteps adds the steps of the transparent ingress.
func (sut *transparentSUT) AddSteps(sc *godog.ScenarioContext) {
	transparentSteps(sc, sut)
}
