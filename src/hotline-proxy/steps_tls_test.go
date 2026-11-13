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

func tlsSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^a proxy certificate for the name hotline-proxy from the test issuer$`, func() error {
		w.proxyCert = w.issuer.leaf("hotline-proxy", time.Now().Add(24*time.Hour), false)
		w.writeKeyPair(w.proxyCert, w.proxyCert)
		return nil
	})
	sc.Step(`^a config file whose tls part points to its cert and key files$`, func() error {
		w.listenTLS, w.tlsCert = "  tls: { cert: app }\n", "  app: { cert: ./tls/cert.pem, key: ./tls/key.pem }\n"
		return nil
	})
	sc.Step(`^an app that trusts the test issuer$`, func() error {
		w.appTLS = w.appTLSConfig()
		return nil
	})
	sc.Step(`^an app that does not trust the test issuer$`, func() error {
		w.appTLS = &tls.Config{RootCAs: x509.NewCertPool(), ServerName: "hotline-proxy", MinVersion: tls.VersionTLS12}
		return nil
	})
	sc.Step(`^an app that allows only TLS 1.1$`, func() error {
		w.appTLS = w.appTLSConfig()
		w.appTLS.MinVersion, w.appTLS.MaxVersion = tls.VersionTLS10, tls.VersionTLS11
		return nil
	})
	sc.Step(`^an app that trusts the test issuer and allows only TLS 1.(2|3)$`, func(minor int) error {
		w.appTLS = w.appTLSConfig()
		w.appTLS.MinVersion = tls.VersionTLS12
		if minor == 3 {
			w.appTLS.MinVersion = tls.VersionTLS13
		}
		w.appTLS.MaxVersion = w.appTLS.MinVersion
		return nil
	})
	sc.Step(`^an app that trusts the test issuer and offers HTTP/2 and HTTP/1.1$`, func() error {
		w.appTLS = w.appTLSConfig()
		w.appTLS.NextProtos = []string{"h2", "http/1.1"}
		return nil
	})
	sc.Step(`^the app sends its call over TLS to the proxy$`, func() error {
		return w.defaultCall()
	})
	sc.Step(`^an app sends its call to the proxy port without TLS$`, func() error {
		return w.defaultCall()
	})
	sc.Step(`^an app opens TLS to the proxy asking for the name api.talon.one$`, func() error {
		// The app skips its own check here, so the test sees whatever the
		// proxy shows for that name.
		w.appTLS = &tls.Config{ServerName: "api.talon.one", InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // looks at the certificate only
		return w.openTLS()
	})
	sc.Step(`^the proxy shows its own certificate$`, func() error {
		return w.showed(w.proxyCert)
	})

	sc.Step(`^the vendor gets the call over https$`, func() error {
		calls := w.vendor("api.talon.one").received()
		if len(calls) != 1 || calls[0].scheme != "https" {
			return failf("the vendor got %v", calls)
		}
		return nil
	})
	sc.Step(`^the app gets the vendor's status, headers and body unchanged$`, func() error {
		if w.tlsErr != nil {
			return w.tlsErr
		}
		if w.respErr != nil {
			return w.respErr
		}
		if w.resp.StatusCode != http.StatusOK || w.resp.Header.Get("X-Request-Id") != "req-5521" || string(w.respBody) != `{"ok":true}` {
			return failf("the app got %d %v %s", w.resp.StatusCode, w.resp.Header, w.respBody)
		}
		return nil
	})
	sc.Step(`^no record is sent$`, func() error {
		if records := w.records(); len(records) != 0 {
			return failf("the receiver got %d records", len(records))
		}
		return nil
	})
	sc.Step(`^the TLS setup fails$`, func() error {
		if w.tlsErr == nil {
			return failf("TLS was set up with version %x", w.tlsState.Version)
		}
		return nil
	})
	sc.Step(`^the proxy logs the failure at debug level$`, func() error {
		// The proxy logs after the app has seen the failure.
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			for _, line := range strings.Split(w.logs.String(), "\n") {
				if strings.Contains(line, "level=DEBUG") && strings.Contains(line, "app TLS setup failed") {
					return nil
				}
			}
		}
		return failf("no debug line about the failure; the log is %s", w.logs.String())
	})
	sc.Step(`^the connection used TLS 1.(2|3)$`, func(minor int) error {
		want := uint16(tls.VersionTLS12)
		if minor == 3 {
			want = tls.VersionTLS13
		}
		if w.tlsState.Version != want {
			return failf("the connection used version %x", w.tlsState.Version)
		}
		return nil
	})
	sc.Step(`^the connection uses HTTP/1.1$`, func() error {
		if w.tlsState.NegotiatedProtocol != "http/1.1" || w.resp == nil || w.resp.Proto != "HTTP/1.1" {
			return failf("the connection agreed on %q", w.tlsState.NegotiatedProtocol)
		}
		return nil
	})

	sc.Step(`^the cert file does not exist$`, func() error {
		w.problem = "cannot be read"
		return os.Remove(filepath.Join(w.dir, "tls", "cert.pem"))
	})
	sc.Step(`^the key file holds no key$`, func() error {
		w.problem = "not a matching certificate and key"
		w.writeFile("tls/key.pem", "not a key\n")
		return nil
	})
	sc.Step(`^the key does not belong to the certificate$`, func() error {
		w.problem = "not a matching certificate and key"
		w.writeKeyPair(w.proxyCert, w.issuer.leaf("hotline-proxy", time.Now().Add(24*time.Hour), false))
		return nil
	})
	sc.Step(`^the certificate has already expired$`, func() error {
		w.problem = "expired"
		expired := w.issuer.leaf("hotline-proxy", time.Now().Add(-time.Hour), false)
		w.writeKeyPair(expired, expired)
		return nil
	})
	sc.Step(`^the tls part has a cert file but no key file$`, func() error {
		w.problem = "cert app needs both cert and key"
		w.listenTLS, w.tlsCert = "  tls: { cert: app }\n", "  app: { cert: ./tls/cert.pem }\n"
		return nil
	})

	sc.Step(`^the app has a connection open to the proxy$`, func() error {
		if err := w.start(); err != nil {
			return err
		}
		w.vendor("api.talon.one")
		conn, err := w.dialProxy()
		if err != nil {
			return errorsJoinTLS(err, w.tlsErr)
		}
		w.openConn = conn
		return w.callOn(conn)
	})
	sc.Step(`^the cert and key files are replaced with a second certificate$`, func() error {
		second := w.issuer.leaf("hotline-proxy", time.Now().Add(24*time.Hour), false)
		w.renewed = &second
		w.replaceKeyPair(second, second)
		return nil
	})
	sc.Step(`^the key file is replaced with a key that does not belong to the certificate$`, func() error {
		other := w.issuer.leaf("hotline-proxy", time.Now().Add(24*time.Hour), false)
		w.replaceFile("tls/key.pem", keyPEM(other))
		return nil
	})
	sc.Step(`^a new connection gets the second certificate$`, func() error {
		if err := w.openTLS(); err != nil {
			return err
		}
		return w.showed(*w.renewed)
	})
	sc.Step(`^a new connection still gets the first certificate$`, func() error {
		if err := w.openTLS(); err != nil {
			return err
		}
		return w.showed(w.proxyCert)
	})
	sc.Step(`^the open connection still takes calls$`, func() error {
		return w.callOn(w.openConn)
	})
}

// appTLSConfig is how an app that trusts the test issuer opens TLS to the
// proxy, by its name hotline-proxy.
func (w *world) appTLSConfig() *tls.Config {
	return &tls.Config{RootCAs: w.issuer.pool, ServerName: "hotline-proxy", MinVersion: tls.VersionTLS12}
}

// openTLS opens a new TLS connection to the proxy and closes it again.
func (w *world) openTLS() error {
	if err := w.start(); err != nil {
		return err
	}
	if w.appTLS == nil {
		w.appTLS = w.appTLSConfig()
	}
	conn, err := w.dialProxy()
	if err != nil {
		return errorsJoinTLS(err, w.tlsErr)
	}
	return conn.Close()
}

// showed checks that the last TLS connection got cert from the proxy.
func (w *world) showed(cert tls.Certificate) error {
	peers := w.tlsState.PeerCertificates
	if len(peers) == 0 || !bytes.Equal(peers[0].Raw, cert.Certificate[0]) {
		return failf("the proxy showed another certificate")
	}
	return nil
}

// callOn makes the world's usual call on a connection that stays open.
func (w *world) callOn(conn net.Conn) error {
	if err := w.exchange(conn, request{
		method: w.method, target: w.target, headers: []string{basic(w.appID + ":x")}, length: -1,
	}); err != nil {
		return err
	}
	if w.respErr != nil {
		return w.respErr
	}
	if w.resp.StatusCode != http.StatusOK {
		return failf("the app got %d", w.resp.StatusCode)
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
func (w *world) writeKeyPair(cert, key tls.Certificate) {
	w.writeFile("tls/cert.pem", certPEM(cert))
	w.writeFile("tls/key.pem", keyPEM(key))
}

// replaceKeyPair swaps both files the way Kubernetes updates a mounted
// Secret: new files are written aside and moved into place.
func (w *world) replaceKeyPair(cert, key tls.Certificate) {
	w.replaceFile("tls/cert.pem", certPEM(cert))
	w.replaceFile("tls/key.pem", keyPEM(key))
}

func (w *world) replaceFile(name, content string) {
	path := w.writeFile(name+".new", content)
	_ = os.Rename(path, filepath.Join(w.dir, name))
}

// errorsJoinTLS reports a failed dial, or why TLS failed to set up.
func errorsJoinTLS(dialErr, tlsErr error) error {
	if errors.Is(dialErr, errTLSSetup) {
		return failf("TLS failed to set up: %v", tlsErr)
	}
	return dialErr
}
