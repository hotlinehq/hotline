package proxy_test

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// placeholder is a {name} in a listeners section the scenario writes: a
// free local port, picked once per name.
var placeholder = regexp.MustCompile(`\{(\w+)\}`)

// listenersSection is the listeners the scenario wrote, with each {name}
// read as a free local address and {admin} as the admin port's.
func (sut *listenersSUT) listenersSection() string {
	if sut.named == nil {
		sut.named = map[string]string{}
	}
	section := placeholder.ReplaceAllStringFunc(sut.listenersRaw, func(m string) string {
		name := m[1 : len(m)-1]
		if name == "dir" {
			return sut.proxy.dir
		}
		if name == "admin" {
			if sut.adminAddr == "" {
				sut.adminAddr = sut.proxy.ports.take()
			}
			return sut.adminAddr
		}
		if _, picked := sut.named[name]; !picked {
			sut.named[name] = sut.proxy.ports.take()
		}
		return sut.named[name]
	})
	if apps, ok := sut.named["apps"]; ok {
		sut.proxy.addr = apps
	}
	var b strings.Builder
	for _, line := range strings.Split(strings.ReplaceAll(section, `\n`, "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// through runs step with the app's calls sent to the named listener.
func (sut *listenersSUT) through(listener string, step func() error) error {
	if err := sut.proxy.start(); err != nil {
		return err
	}
	before := sut.proxy.addr
	sut.proxy.addr = sut.named[listener]
	defer func() { sut.proxy.addr = before }()
	return step()
}

// adminTLSGet asks the admin port path over TLS, as a health check does,
// without checking the certificate.
func (sut *listenersSUT) adminTLSGet(path string) error {
	if err := sut.proxy.start(); err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // a health check does not check
	}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+sut.adminAddr+path, nil)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	sut.app.resp = resp
	sut.app.tlsState = *resp.TLS
	return nil
}

func listenerSteps(sc *godog.ScenarioContext, sut *listenersSUT) {
	sc.Step(`^the listeners are:$`, func(doc *godog.DocString) error {
		sut.listenersRaw = doc.Content
		// The listeners written here replace the proxy port of the config.
		sut.proxy.noProxyPort = true
		return nil
	})
	sc.Step(`^the certs are:$`, func(doc *godog.DocString) error {
		var entries []string
		for _, line := range strings.Split(doc.Content, "\n") {
			entries = append(entries, "  "+line+"\n")
		}
		sut.certs.replaceEntries(entries...)
		return nil
	})
	sc.Step(`^the app calls Talon.One through the listener (\S+)$`, func(listener string) error {
		return sut.through(listener, func() error { return sut.app.defaultCall() })
	})
	sc.Step(`^the proxy sent (\d+) calls to Talon.One$`, func(want int) error {
		if got := len(sut.vendors.vendor("api.talon.one").received()); got != want {
			return failf("Talon.One got %d calls, want %d", got, want)
		}
		return nil
	})
	sc.Step(`^an app that trusts the interception certificate authority (?:opens|has opened) TLS to the (socket )?listener (\S+) asking for (?:the name (\S+)|no name)$`, func(socket, listener, name string) error {
		return sut.through(listener, func() error {
			roots := x509.NewCertPool()
			roots.AddCert(sut.certs.interceptionAuthority().cert)
			if sut.certs.replacement() != nil {
				roots.AddCert(sut.certs.replacement().cert)
			}
			network, address := "tcp", sut.proxy.addr
			if socket != "" {
				network, address = "unix", sut.proxy.dir+"/"+listener+".sock"
			}
			conn, err := (&net.Dialer{}).DialContext(context.Background(), network, address)
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			config := &tls.Config{RootCAs: roots, ServerName: name, MinVersion: tls.VersionTLS12}
			switch {
			case name != "":
			case socket == "":
				// Go's client sends no site name for an address.
				config.ServerName = "127.0.0.1"
			default:
				// Nor for a socket file; the Then step checks the
				// signature itself.
				config.InsecureSkipVerify = true
			}
			client := tls.Client(conn, config)
			_ = client.SetDeadline(time.Now().Add(10 * time.Second))
			sut.app.tunnelErr = client.HandshakeContext(context.Background())
			if sut.app.tunnelErr == nil {
				sut.app.tunnelCerts = append(sut.app.tunnelCerts, client.ConnectionState().PeerCertificates[0])
			}
			return nil
		})
	})
	sc.Step(`^an app opens TLS to the listener (\S+) asking for the name api.talon.one$`, func(listener string) error {
		return sut.through(listener, func() error {
			sut.app.appTLS = &tls.Config{ServerName: "api.talon.one", InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // looks at the certificate only
			return sut.tlsIngress.openTLS()
		})
	})
	sc.Step(`^the app sees a certificate for the local address 127.0.0.1 signed by the interception certificate authority$`, func() error {
		if sut.app.tunnelErr != nil {
			return failf("TLS with the listener failed: %v", sut.app.tunnelErr)
		}
		leaf := sut.app.tunnelCerts[len(sut.app.tunnelCerts)-1]
		if len(leaf.IPAddresses) != 1 || !leaf.IPAddresses[0].Equal(net.IPv4(127, 0, 0, 1)) {
			return failf("the certificate names the IPs %v", leaf.IPAddresses)
		}
		return leaf.CheckSignatureFrom(sut.certs.interceptionAuthority().cert)
	})
	sc.Step(`^the app sees a certificate for the name localhost signed by the interception certificate authority$`, func() error {
		if sut.app.tunnelErr != nil {
			return failf("TLS with the listener failed: %v", sut.app.tunnelErr)
		}
		leaf := sut.app.tunnelCerts[len(sut.app.tunnelCerts)-1]
		if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "localhost" {
			return failf("the certificate names %v", leaf.DNSNames)
		}
		return leaf.CheckSignatureFrom(sut.certs.interceptionAuthority().cert)
	})
	sc.Step(`^hotline.certificate.not_after is reported for (.+)$`, func(uses string) error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		for _, use := range strings.Split(strings.ReplaceAll(uses, " and ", ", "), ", ") {
			err := sut.proxy.eventually(func() error {
				if p, ok := sut.telemetry.latest("hotline.certificate.not_after", parseLabels("hotline.certificate.use "+use)); !ok || p.value <= 0 {
					return failf("hotline.certificate.not_after has no value for %s; got %v", use, sut.telemetry.lastPoints("hotline.certificate.not_after"))
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the app opens a tunnel to (\S+):443 through the listener (\S+)$`, func(host, listener string) error {
		return sut.through(listener, func() error { return sut.connect.openTunnel(host, "443", "") })
	})
	sc.Step(`^the app sends "([^"]*)" to the listener (\S+)$`, func(message, listener string) error {
		return sut.through(listener, func() error {
			conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", sut.proxy.addr)
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			if _, err := io.WriteString(conn, message); err != nil {
				return err
			}
			sut.app.respBody, sut.app.respErr = io.ReadAll(conn)
			return nil
		})
	})
	sc.Step(`^the capture listener has protocol tcp and no cert$`, func() error {
		sut.capture.overTCPWithNoCert(sut.proxy.dir + "/capture.sock")
		return nil
	})
	sc.Step(`^the admin port has tls: \{ cert: (\S+) \}$`, func(cert string) error {
		sut.adminAddr = sut.proxy.ports.take()
		sut.adminTLS = "  tls: { cert: " + cert + " }\n"
		return nil
	})
	sc.Step(`^the admin port has the address "([^"]*)"$`, func(address string) error {
		sut.adminRaw = address
		return nil
	})
	sc.Step(`^a health check asks /readyz over TLS$`, func() error {
		return sut.adminTLSGet("/readyz")
	})
	sc.Step(`^something connects to the admin port and hangs up before TLS is set up$`, func() error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", sut.adminAddr)
		if err != nil {
			return err
		}
		return conn.Close()
	})
	sc.Step(`^the proxy's HTTP server logged a warning about "([^"]+)"$`, func(text string) error {
		sut.proxy.settle()
		for _, line := range strings.Split(sut.proxy.logs.String(), "\n") {
			if strings.Contains(line, `level=WARN msg="http server"`) && strings.Contains(line, text) {
				return nil
			}
		}
		return failf("no warning from the proxy's HTTP server about %q in:\n%s", text, sut.proxy.logs.String())
	})
	sc.Step(`^it gets (\d+)$`, func(status int) error {
		if sut.app.resp == nil || sut.app.resp.StatusCode != status {
			return failf("the health check got %v, want %d", sut.app.resp, status)
		}
		return nil
	})
	sc.Step(`^the admin port shows the renewed certificate$`, func() error {
		renewed, _ := x509.ParseCertificate(sut.tlsIngress.renewedCertificate().Certificate[0])
		if got := sut.app.tlsState.PeerCertificates[0]; !got.Equal(renewed) {
			return failf("the admin port shows the certificate valid to %s", got.NotAfter)
		}
		return nil
	})
}

// listenersSUT is the listeners section as a scenario writes it, and the
// admin port.
type listenersSUT struct {
	proxy      *proxySUT
	vendors    *vendorsSUT
	certs      *certsSUT
	app        *appSUT
	tlsIngress *tlsIngressSUT
	connect    *connectSUT
	telemetry  *telemetrySUT
	capture    *captureSUT
	// adminAddr is the admin listen address; any free port when empty.
	// adminRaw, when set, is the admin address as written, scheme and all,
	// and adminTLS the admin port's tls line. noAdmin leaves the admin
	// section out.
	adminAddr string
	adminRaw  string
	adminTLS  string
	noAdmin   bool
	// listenersRaw, when set, is the listeners section the scenario wrote,
	// and named the address each {name} in it stands for.
	listenersRaw string
	named        map[string]string
}

func newListenersSUT(root *proxySUT, vendors *vendorsSUT, certs *certsSUT, app *appSUT, tlsIngress *tlsIngressSUT, connect *connectSUT, telemetry *telemetrySUT, capture *captureSUT) *listenersSUT {
	sut := &listenersSUT{proxy: root, vendors: vendors, certs: certs, app: app, tlsIngress: tlsIngress, connect: connect, telemetry: telemetry, capture: capture}
	root.addConfig(configAdmin, sut.adminSection)
	root.addConfig(configListeners, func() string {
		if sut.listenersRaw == "" {
			return ""
		}
		return sut.listenersSection()
	})
	return sut
}

// AddSteps adds the steps of the listeners and the admin port.
func (sut *listenersSUT) AddSteps(sc *godog.ScenarioContext) {
	listenerSteps(sc, sut)
}

// adminSection is the config's admin section. The listeners the scenario
// wrote are read first, as they may name {admin}, which picks the admin
// port.
func (sut *listenersSUT) adminSection() string {
	if sut.listenersRaw != "" {
		sut.listenersSection()
	}
	if sut.noAdmin {
		return ""
	}
	address := cmp.Or(sut.adminAddr, "127.0.0.1:0")
	return fmt.Sprintf("admin:\n  address: %q\n%s", cmp.Or(sut.adminRaw, "tcp://"+address), sut.adminTLS)
}

// useAdminAddress gives the admin port address, and leaveOutAdmin leaves
// the admin section out.
func (sut *listenersSUT) useAdminAddress(address string) { sut.adminAddr = address }
func (sut *listenersSUT) leaveOutAdmin()                 { sut.noAdmin = true }
