package proxy_test

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

func tlsSteps(sc *godog.ScenarioContext, sut *tlsIngressSUT) {
	sc.Step(`^a proxy certificate for the name hotline-proxy from the test certificate authority$`, func() error {
		sut.proxyCert = sut.vendors.authority.leaf("hotline-proxy", time.Now().Add(24*time.Hour), false)
		sut.writeKeyPair(sut.proxyCert, sut.proxyCert)
		return nil
	})
	sc.Step(`^a config file whose tls part points to its cert and key files$`, func() error {
		sut.useAppCertificate()
		return nil
	})
	sc.Step(`^an app that trusts the test certificate authority$`, func() error {
		sut.app.appTLS = sut.appTLSConfig()
		return nil
	})
	sc.Step(`^an app that does not trust the test certificate authority$`, func() error {
		sut.app.appTLS = &tls.Config{RootCAs: x509.NewCertPool(), ServerName: "hotline-proxy", MinVersion: tls.VersionTLS12}
		return nil
	})
	sc.Step(`^an app that allows only TLS 1.1$`, func() error {
		sut.app.appTLS = sut.appTLSConfig()
		sut.app.appTLS.MinVersion, sut.app.appTLS.MaxVersion = tls.VersionTLS10, tls.VersionTLS11
		return nil
	})
	sc.Step(`^an app that trusts the test certificate authority and allows only TLS 1.(2|3)$`, func(minor int) error {
		sut.app.appTLS = sut.appTLSConfig()
		sut.app.appTLS.MinVersion = tls.VersionTLS12
		if minor == 3 {
			sut.app.appTLS.MinVersion = tls.VersionTLS13
		}
		sut.app.appTLS.MaxVersion = sut.app.appTLS.MinVersion
		return nil
	})
	sc.Step(`^an app that trusts the test certificate authority and offers HTTP/2 and HTTP/1.1$`, func() error {
		sut.app.appTLS = sut.appTLSConfig()
		sut.app.appTLS.NextProtos = []string{"h2", "http/1.1"}
		return nil
	})
	sc.Step(`^the app sends its call over TLS to the proxy$`, func() error {
		return sut.app.defaultCall()
	})
	sc.Step(`^an app sends its call to the proxy port without TLS$`, func() error {
		return sut.app.defaultCall()
	})
	sc.Step(`^an app opens TLS to the proxy asking for the name api.talon.one$`, func() error {
		// The app skips its own check here, so the test sees whatever the
		// proxy shows for that name.
		sut.app.appTLS = &tls.Config{ServerName: "api.talon.one", InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // looks at the certificate only
		return sut.openTLS()
	})
	sc.Step(`^the proxy shows its own certificate$`, func() error {
		return sut.showed(sut.proxyCert)
	})

	sc.Step(`^the vendor gets the call over https$`, func() error {
		calls := sut.vendors.vendor("api.talon.one").received()
		if len(calls) != 1 || calls[0].scheme != "https" {
			return failf("the vendor got %v", calls)
		}
		return nil
	})
	sc.Step(`^the app gets the vendor's status, headers and body unchanged$`, func() error {
		if sut.app.tlsErr != nil {
			return sut.app.tlsErr
		}
		if sut.app.respErr != nil {
			return sut.app.respErr
		}
		if sut.app.resp.StatusCode != http.StatusOK || sut.app.resp.Header.Get("X-Request-Id") != "req-5521" || string(sut.app.respBody) != `{"ok":true}` {
			return failf("the app got %d %v %s", sut.app.resp.StatusCode, sut.app.resp.Header, sut.app.respBody)
		}
		return nil
	})
	sc.Step(`^no record is sent$`, func() error {
		if records := sut.otlp.records(); len(records) != 0 {
			return failf("the receiver got %d records", len(records))
		}
		return nil
	})
	sc.Step(`^the TLS setup fails$`, func() error {
		if sut.app.tlsErr == nil {
			return failf("TLS was set up with version %x", sut.app.tlsState.Version)
		}
		return nil
	})
	sc.Step(`^the proxy logs the failure at debug level$`, func() error {
		// The proxy logs after the app has seen the failure.
		for deadline := time.Now().Add(waitLimit); time.Now().Before(deadline); pause(5 * time.Millisecond) {
			for _, line := range strings.Split(sut.proxy.logs.String(), "\n") {
				if strings.Contains(line, "level=DEBUG") && strings.Contains(line, "app TLS setup failed") {
					return nil
				}
			}
		}
		return failf("no debug line about the failure; the log is %s", sut.proxy.logs.String())
	})
	sc.Step(`^the connection used TLS 1.(2|3)$`, func(minor int) error {
		want := uint16(tls.VersionTLS12)
		if minor == 3 {
			want = tls.VersionTLS13
		}
		if sut.app.tlsState.Version != want {
			return failf("the connection used version %x", sut.app.tlsState.Version)
		}
		return nil
	})
	sc.Step(`^the connection uses HTTP/1.1$`, func() error {
		if sut.app.tlsState.NegotiatedProtocol != "http/1.1" || sut.app.resp == nil || sut.app.resp.Proto != "HTTP/1.1" {
			return failf("the connection agreed on %q", sut.app.tlsState.NegotiatedProtocol)
		}
		return nil
	})

	sc.Step(`^the cert file does not exist$`, func() error {
		sut.proxy.problem = "cannot be read"
		return os.Remove(filepath.Join(sut.proxy.dir, "tls", "cert.pem"))
	})
	sc.Step(`^the key file holds no key$`, func() error {
		sut.proxy.problem = "not a matching certificate and key"
		sut.proxy.writeFile("tls/key.pem", "not a key\n")
		return nil
	})
	sc.Step(`^the key does not belong to the certificate$`, func() error {
		sut.proxy.problem = "not a matching certificate and key"
		sut.writeKeyPair(sut.proxyCert, sut.vendors.authority.leaf("hotline-proxy", time.Now().Add(24*time.Hour), false))
		return nil
	})
	sc.Step(`^the certificate has already expired$`, func() error {
		sut.proxy.problem = "expired"
		expired := sut.vendors.authority.leaf("hotline-proxy", time.Now().Add(-time.Hour), false)
		sut.writeKeyPair(expired, expired)
		return nil
	})
	sc.Step(`^the tls part has a cert file but no key file$`, func() error {
		sut.proxy.problem = "cert app needs both cert and key"
		sut.listenTLS, sut.tlsCert = "  tls: { cert: app }\n", "  app: { cert: ./tls/cert.pem }\n"
		return nil
	})

	sc.Step(`^the app has a connection open to the proxy$`, func() error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		sut.vendors.vendor("api.talon.one")
		conn, err := sut.app.dialProxy()
		if err != nil {
			return errorsJoinTLS(err, sut.app.tlsErr)
		}
		sut.app.openConn = conn
		return sut.callOn(conn)
	})
	sc.Step(`^the cert and key files are replaced with a second certificate$`, func() error {
		second := sut.vendors.authority.leaf("hotline-proxy", time.Now().Add(24*time.Hour), false)
		sut.renewed = &second
		sut.replaceKeyPair(second, second)
		return nil
	})
	sc.Step(`^the key file is replaced with a key that does not belong to the certificate$`, func() error {
		other := sut.vendors.authority.leaf("hotline-proxy", time.Now().Add(24*time.Hour), false)
		sut.proxy.replaceFile("tls/key.pem", keyPEM(other))
		return nil
	})
	sc.Step(`^a new connection gets the second certificate$`, func() error {
		if err := sut.openTLS(); err != nil {
			return err
		}
		return sut.showed(*sut.renewed)
	})
	sc.Step(`^a new connection still gets the first certificate$`, func() error {
		if err := sut.openTLS(); err != nil {
			return err
		}
		return sut.showed(sut.proxyCert)
	})
	sc.Step(`^the open connection still takes calls$`, func() error {
		return sut.callOn(sut.app.openConn)
	})
}

// appTLSConfig is how an app that trusts the test certificate authority opens TLS to the
// proxy, by its name hotline-proxy.
func (sut *tlsIngressSUT) appTLSConfig() *tls.Config {
	return &tls.Config{RootCAs: sut.vendors.authority.pool, ServerName: "hotline-proxy", MinVersion: tls.VersionTLS12}
}

// openTLS opens a new TLS connection to the proxy and closes it again.
func (sut *tlsIngressSUT) openTLS() error {
	if err := sut.proxy.start(); err != nil {
		return err
	}
	if sut.app.appTLS == nil {
		sut.app.appTLS = sut.appTLSConfig()
	}
	conn, err := sut.app.dialProxy()
	if err != nil {
		return errorsJoinTLS(err, sut.app.tlsErr)
	}
	return conn.Close()
}

// showed checks that the last TLS connection got cert from the proxy.
func (sut *tlsIngressSUT) showed(cert tls.Certificate) error {
	peers := sut.app.tlsState.PeerCertificates
	if len(peers) == 0 || !bytes.Equal(peers[0].Raw, cert.Certificate[0]) {
		return failf("the proxy showed another certificate")
	}
	return nil
}

// callOn makes the world's usual call on a connection that stays open.
func (sut *tlsIngressSUT) callOn(conn net.Conn) error {
	if err := sut.app.exchange(conn, request{
		method: sut.app.method, target: sut.app.target, headers: []string{basic(sut.app.appID + ":x")}, length: -1,
	}); err != nil {
		return err
	}
	if sut.app.respErr != nil {
		return sut.app.respErr
	}
	if sut.app.resp.StatusCode != http.StatusOK {
		return failf("the app got %d", sut.app.resp.StatusCode)
	}
	return nil
}

func certPEM(cert tls.Certificate) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}))
}

func keyPEM(cert tls.Certificate) string {
	der, _ := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// writeKeyPair writes the certificate of cert and the key of key to the
// proxy's cert and key files.
func (sut *tlsIngressSUT) writeKeyPair(cert, key tls.Certificate) {
	sut.proxy.writeFile("tls/cert.pem", certPEM(cert))
	sut.proxy.writeFile("tls/key.pem", keyPEM(key))
}

// replaceKeyPair swaps both files the way Kubernetes updates a mounted
// Secret: new files are written aside and moved into place.
func (sut *tlsIngressSUT) replaceKeyPair(cert, key tls.Certificate) {
	sut.proxy.replaceFile("tls/cert.pem", certPEM(cert))
	sut.proxy.replaceFile("tls/key.pem", keyPEM(key))
}

func (sut *proxySUT) replaceFile(name, content string) {
	path := sut.writeFile(name+".new", content)
	_ = os.Rename(path, filepath.Join(sut.dir, name))
}

// errorsJoinTLS reports a failed dial, or why TLS failed to set up.
func errorsJoinTLS(dialErr, tlsErr error) error {
	if errors.Is(dialErr, errTLSSetup) {
		return failf("TLS failed to set up: %v", tlsErr)
	}
	return dialErr
}

// tlsIngressSUT is the TLS ingress: the proxy port with TLS, and the
// certificate the proxy shows apps there.
type tlsIngressSUT struct {
	proxy   *proxySUT
	vendors *vendorsSUT
	app     *appSUT
	otlp    *otlpSUT
	// listenTLS, when set, is the tls line of the proxy listener, and
	// tlsCert the certs entry of the certificate apps are shown over TLS.
	listenTLS string
	tlsCert   string
	// proxyCert is the certificate the proxy's cert and key files hold, and
	// renewed the one that replaced it in its files.
	proxyCert tls.Certificate
	renewed   *tls.Certificate
}

func newTLSIngressSUT(root *proxySUT, vendors *vendorsSUT, app *appSUT, otlp *otlpSUT) *tlsIngressSUT {
	sut := &tlsIngressSUT{proxy: root, vendors: vendors, app: app, otlp: otlp}
	root.addConfig(configProxyListener, func() string { return sut.listenTLS })
	root.addConfig(configCerts, func() string { return sut.tlsCert })
	return sut
}

// useAppCertificate turns on TLS on the proxy port, with the certificate
// named app, whose files are tls/cert.pem and tls/key.pem.
func (sut *tlsIngressSUT) useAppCertificate() {
	sut.listenTLS, sut.tlsCert = "  tls: { cert: app }\n", "  app: { cert: ./tls/cert.pem, key: ./tls/key.pem }\n"
}

// AddSteps adds the steps of the TLS ingress.
func (sut *tlsIngressSUT) AddSteps(sc *godog.ScenarioContext) {
	tlsSteps(sc, sut)
}
