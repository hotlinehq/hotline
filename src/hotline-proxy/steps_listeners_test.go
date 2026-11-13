package proxy_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
func (w *world) listenersSection() string {
	if w.named == nil {
		w.named = map[string]string{}
	}
	section := placeholder.ReplaceAllStringFunc(w.listenersRaw, func(m string) string {
		name := m[1 : len(m)-1]
		if name == "dir" {
			return w.dir
		}
		if name == "admin" {
			if w.adminAddr == "" {
				w.adminAddr = freePort()
			}
			return w.adminAddr
		}
		if _, picked := w.named[name]; !picked {
			w.named[name] = freePort()
		}
		return w.named[name]
	})
	if apps, ok := w.named["apps"]; ok {
		w.addr = apps
	}
	var b strings.Builder
	for _, line := range strings.Split(strings.ReplaceAll(section, `\n`, "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// through runs step with the app's calls sent to the named listener.
func (w *world) through(listener string, step func() error) error {
	if err := w.start(); err != nil {
		return err
	}
	before := w.addr
	w.addr = w.named[listener]
	defer func() { w.addr = before }()
	return step()
}

// adminTLSGet asks the admin port path over TLS, as a health check does,
// without checking the certificate.
func (w *world) adminTLSGet(path string) error {
	if err := w.start(); err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // a health check does not check
	}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+w.adminAddr+path, nil)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	w.resp = resp
	w.tlsState = *resp.TLS
	return nil
}

func listenerSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the listeners are:$`, func(doc *godog.DocString) error {
		w.listenersRaw = doc.Content
		return nil
	})
	sc.Step(`^the certs are:$`, func(doc *godog.DocString) error {
		w.issuersSection = "certs:\n"
		for _, line := range strings.Split(doc.Content, "\n") {
			w.issuersSection += "  " + line + "\n"
		}
		return nil
	})
	sc.Step(`^the app calls Talon.One through the listener (\S+)$`, func(listener string) error {
		return w.through(listener, func() error { return w.defaultCall() })
	})
	sc.Step(`^the proxy sent (\d+) calls to Talon.One$`, func(want int) error {
		if got := len(w.vendor("api.talon.one").received()); got != want {
			return failf("Talon.One got %d calls, want %d", got, want)
		}
		return nil
	})
	sc.Step(`^an app that trusts the interception issuer (?:opens|has opened) TLS to the (socket )?listener (\S+) asking for (?:the name (\S+)|no name)$`, func(socket, listener, name string) error {
		return w.through(listener, func() error {
			roots := x509.NewCertPool()
			roots.AddCert(w.interception.cert)
			if w.second != nil {
				roots.AddCert(w.second.cert)
			}
			network, address := "tcp", w.addr
			if socket != "" {
				network, address = "unix", w.dir+"/"+listener+".sock"
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
				config.InsecureSkipVerify = true //nolint:gosec // checked by the Then step
			}
			client := tls.Client(conn, config)
			_ = client.SetDeadline(time.Now().Add(10 * time.Second))
			w.tunnelErr = client.HandshakeContext(context.Background())
			if w.tunnelErr == nil {
				w.tunnelCerts = append(w.tunnelCerts, client.ConnectionState().PeerCertificates[0])
			}
			return nil
		})
	})
	sc.Step(`^an app opens TLS to the listener (\S+) asking for the name api.talon.one$`, func(listener string) error {
		return w.through(listener, func() error {
			w.appTLS = &tls.Config{ServerName: "api.talon.one", InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // looks at the certificate only
			return w.openTLS()
		})
	})
	sc.Step(`^the app sees a certificate for the local address 127.0.0.1 signed by the interception issuer$`, func() error {
		if w.tunnelErr != nil {
			return failf("TLS with the listener failed: %v", w.tunnelErr)
		}
		leaf := w.tunnelCerts[len(w.tunnelCerts)-1]
		if len(leaf.IPAddresses) != 1 || !leaf.IPAddresses[0].Equal(net.IPv4(127, 0, 0, 1)) {
			return failf("the certificate names the IPs %v", leaf.IPAddresses)
		}
		return leaf.CheckSignatureFrom(w.interception.cert)
	})
	sc.Step(`^the app sees a certificate for the name localhost signed by the interception issuer$`, func() error {
		if w.tunnelErr != nil {
			return failf("TLS with the listener failed: %v", w.tunnelErr)
		}
		leaf := w.tunnelCerts[len(w.tunnelCerts)-1]
		if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "localhost" {
			return failf("the certificate names %v", leaf.DNSNames)
		}
		return leaf.CheckSignatureFrom(w.interception.cert)
	})
	sc.Step(`^hotline.certificate.not_after is reported for (.+)$`, func(uses string) error {
		if err := w.start(); err != nil {
			return err
		}
		for _, use := range strings.Split(strings.ReplaceAll(uses, " and ", ", "), ", ") {
			err := w.eventually(func() error {
				if p, ok := w.latest("hotline.certificate.not_after", parseLabels("hotline.certificate.use "+use)); !ok || p.value <= 0 {
					return failf("hotline.certificate.not_after has no value for %s; got %v", use, w.lastPoints("hotline.certificate.not_after"))
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
		return w.through(listener, func() error { return w.openTunnel(host, "443", "") })
	})
	sc.Step(`^the app sends "([^"]*)" to the listener (\S+)$`, func(message, listener string) error {
		return w.through(listener, func() error {
			conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", w.addr)
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			if _, err := io.WriteString(conn, message); err != nil {
				return err
			}
			w.respBody, w.respErr = io.ReadAll(conn)
			return nil
		})
	})
	sc.Step(`^the capture listener has protocol tcp and no cert$`, func() error {
		w.captureFile = w.dir + "/capture.sock"
		w.captureProtocol, w.captureIssuer = "    protocol: tcp\n", " "
		return nil
	})
	sc.Step(`^the admin port has tls: \{ cert: (\S+) \}$`, func(cert string) error {
		w.adminAddr = freePort()
		w.adminTLS = "  tls: { cert: " + cert + " }\n"
		return nil
	})
	sc.Step(`^the admin port has the address "([^"]*)"$`, func(address string) error {
		w.adminRaw = address
		return nil
	})
	sc.Step(`^a health check asks /readyz over TLS$`, func() error {
		return w.adminTLSGet("/readyz")
	})
	sc.Step(`^it gets (\d+)$`, func(status int) error {
		if w.resp == nil || w.resp.StatusCode != status {
			return failf("the health check got %v, want %d", w.resp, status)
		}
		return nil
	})
	sc.Step(`^the admin port shows the renewed certificate$`, func() error {
		renewed, _ := x509.ParseCertificate(w.renewed.Certificate[0])
		if got := w.tlsState.PeerCertificates[0]; !got.Equal(renewed) {
			return failf("the admin port shows the certificate valid to %s", got.NotAfter)
		}
		return nil
	})
}
