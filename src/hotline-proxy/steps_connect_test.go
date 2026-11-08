package proxy_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

func connectSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^an interception issuer named customer in issuers, which listen.connect picks$`, func() error {
		w.useInterceptionIssuer(newIssuer())
		return nil
	})
	sc.Step(`^an interception issuer valid for two years$`, func() error {
		w.useInterceptionIssuer(newIssuerValid(time.Now().Add(-time.Hour), time.Now().Add(2*365*24*time.Hour)))
		return nil
	})
	sc.Step(`^an app that trusts the interception issuer$`, func() error {
		w.tunnelRoots = x509.NewCertPool()
		w.tunnelRoots.AddCert(w.interception.cert)
		return nil
	})
	sc.Step(`^an app that does not trust the interception issuer$`, func() error {
		w.tunnelRoots = x509.NewCertPool()
		return nil
	})
	sc.Step(`^the app offers only (HTTP/1.1|HTTP/2) inside the tunnel$`, func(version string) error {
		w.tunnelProtos = []string{map[string]string{"HTTP/1.1": "http/1.1", "HTTP/2": "h2"}[version]}
		return nil
	})

	sc.Step(`^the app calls https://(\S+) through the proxy as integration (\S+)$`, func(host, id string) error {
		return w.callThroughProxy("http", host, id)
	})
	sc.Step(`^the app calls https://(\S+) through the proxy with no ID$`, func(host string) error {
		return w.callThroughProxy("http", host, "")
	})
	sc.Step(`^the app calls https://(\S+) through an https proxy address as integration (\S+)$`, func(host, id string) error {
		return w.callThroughProxy("https", host, id)
	})
	sc.Step(`^the app (?:opens|has opened) a tunnel to (\S+):(\d+)$`, func(host, port string) error {
		return w.openTunnel(host, port, "")
	})
	sc.Step(`^the app opens a tunnel to (\S+):(\d+) as integration (\S+)$`, w.openTunnel)
	sc.Step(`^the app (?:opens|has opened) a tunnel to (\S+):(\d+) as integration (\S+) over HTTP/2$`, w.openTunnelHTTP2)
	sc.Step(`^the app has opened a tunnel to (\S+):(\d+) over (HTTP/1.1|HTTP/2) and sent nothing in it$`, func(host, port, version string) error {
		if version == "HTTP/2" {
			return w.openTunnelHTTP2(host, port, "")
		}
		return w.openTunnel(host, port, "")
	})
	sc.Step(`^the app opens two tunnels to (\S+):(\d+) over one HTTP/2 connection$`, func(host, port string) error {
		for range 2 {
			if err := w.openTunnelHTTP2(host, port, ""); err != nil {
				return err
			}
		}
		if !w.h2Reused {
			return failf("the second tunnel opened its own connection")
		}
		return nil
	})
	sc.Step(`^an app sends "CONNECT (\S+)" over plain HTTP/2$`, func(authority string) error {
		return w.sendHTTP2(http.MethodConnect, "", authority)
	})
	sc.Step(`^the proxy stops$`, func() error {
		stopped := make(chan struct{})
		go func() {
			w.settle()
			close(stopped)
		}()
		select {
		case <-stopped:
			return nil
		case <-time.After(2 * time.Second):
			return failf("the proxy took over 2 seconds to stop")
		}
	})
	sc.Step(`^the tunnel is closed$`, func() error {
		_ = w.tunnel.SetReadDeadline(time.Now().Add(2 * time.Second))
		if n, err := w.tunnel.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
			return failf("the tunnel gave %d bytes and %v, want it closed", n, err)
		}
		return nil
	})
	sc.Step(`^the app opens two tunnels to (\S+):(\d+)$`, func(host, port string) error {
		for range 2 {
			if err := w.openTunnel(host, port, ""); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^sends a call inside it with Proxy-Authorization for (\S+)$`, func(id string) error {
		return w.callInTunnel("Host: api.talon.one", basic(id+":x"))
	})
	sc.Step(`^sends a call inside it with the header "([^"]+)"$`, func(header string) error {
		return w.callInTunnel(header)
	})
	sc.Step(`^sends a plain HTTP call inside it$`, func() error {
		return w.callInTunnel("Host: api.talon.one")
	})
	sc.Step(`^the app sends two calls through one tunnel$`, func() error {
		if err := w.openTunnel("api.talon.one", "443", "talon-shop-a"); err != nil {
			return err
		}
		for range 2 {
			if err := w.callInTunnel("Host: api.talon.one"); err != nil {
				return err
			}
			if err := w.waitForRecords(len(w.sentRecords) + 1); err != nil {
				return err
			}
			records, _ := w.receivers[0].got()
			w.sentRecords = records
		}
		return nil
	})
	sc.Step(`^the app sends two calls on one TLS connection$`, func() error {
		if err := w.start(); err != nil {
			return err
		}
		w.vendor("api.talon.one")
		conn, err := w.dialProxy()
		if err != nil {
			return errorsJoinTLS(err, w.tlsErr)
		}
		w.openConn = conn
		for range 2 {
			if err := w.callOn(conn); err != nil {
				return err
			}
			if err := w.waitForRecords(len(w.sentRecords) + 1); err != nil {
				return err
			}
			records, _ := w.receivers[0].got()
			w.sentRecords = records
		}
		return nil
	})
	sc.Step(`^the app sends a signed call to https://api.talon.one through a tunnel$`, func() error {
		if err := w.openTunnel("api.talon.one", "443", "talon-shop-a"); err != nil {
			return err
		}
		return w.exchange(w.tunnel, request{
			method:  "POST",
			target:  signedPath,
			headers: signedHeaders(),
			body:    strings.NewReader(signedBody),
			length:  len(signedBody),
		})
	})
	sc.Step(`^the files of the issuer named customer are replaced with a second issuer$`, func() error {
		second := newIssuer()
		w.replaceFile("ca/cert.pem", issuerCertPEM(second))
		w.replaceFile("ca/key.pem", issuerKeyPEM(second))
		w.tunnelRoots.AddCert(second.cert)
		w.second = second
		return nil
	})

	sc.Step(`^the test vendor got the call over https with its Host, path, query, headers and body unchanged$`, func() error {
		c, err := w.onlyCall("api.talon.one")
		if err != nil {
			return err
		}
		if c.scheme != "https" || c.port != "443" || c.host != "api.talon.one" ||
			c.uri != "/v2/customer_sessions/sess-8812?dry=true" ||
			c.header.Get("X-Shop") != "shop-a" || string(c.body) != `{"profileId":"p-1"}` {
			return failf("the vendor got %+v", c)
		}
		return nil
	})
	sc.Step(`^the test vendor never saw Proxy-Authorization$`, func() error {
		c, err := w.onlyCall("api.talon.one")
		if err != nil {
			return err
		}
		if got := c.header.Values("Proxy-Authorization"); len(got) > 0 {
			return failf("the vendor got Proxy-Authorization %v", got)
		}
		return nil
	})
	sc.Step(`^the test vendor got the same Host, path, query, body and signed headers the app signed$`, func() error {
		c, err := w.onlyCall("api.talon.one")
		if err != nil {
			return err
		}
		if c.host != "api.talon.one" || c.uri != signedPath || string(c.body) != signedBody {
			return failf("the vendor got Host %s, %s and %q", c.host, c.uri, c.body)
		}
		for _, h := range signedHeaders() {
			name, value, _ := strings.Cut(h, ": ")
			if got := c.header.Get(name); name != "Host" && got != value {
				return failf("the vendor got %s %q, want %q", name, got, value)
			}
		}
		return nil
	})
	sc.Step(`^the test vendor for (\S+) got the call$`, func(host string) error {
		_, err := w.onlyCall(host)
		return err
	})
	sc.Step(`^the test vendor got the call over https on port 443$`, func() error {
		c, err := w.onlyCall("api.talon.one")
		if err != nil {
			return err
		}
		if c.scheme != "https" || c.port != "443" {
			return failf("the vendor got the call over %s on port %s", c.scheme, c.port)
		}
		return nil
	})
	sc.Step(`^the record says hotline.ingress (\S+) and ID (\S+)$`, func(ingress, id string) error {
		if err := w.lastHas("hotline.ingress", ingress); err != nil {
			return err
		}
		return w.lastHas("hotline.integration.id", id)
	})
	sc.Step(`^the record has url.template (\S+)$`, func(template string) error {
		return w.lastHas("url.template", template)
	})
	sc.Step(`^the call inside the tunnel used (HTTP/1.1|HTTP/2)$`, func(version string) error {
		want := map[string]string{"HTTP/1.1": "HTTP/1.1", "HTTP/2": "HTTP/2.0"}[version]
		if w.resp == nil || w.resp.Proto != want {
			return failf("the app got %v, want %s", w.resp, want)
		}
		return nil
	})
	sc.Step(`^the app sees a certificate for (\S+) signed by the (interception|second) issuer$`, func(host, which string) error {
		signer := w.interception
		if which == "second" {
			signer = w.second
		}
		if w.tunnelErr != nil {
			return failf("TLS inside the tunnel failed: %v", w.tunnelErr)
		}
		leaf := w.tunnelCerts[len(w.tunnelCerts)-1]
		if !slices.Contains(leaf.DNSNames, host) {
			return failf("the certificate names %v, want %s", leaf.DNSNames, host)
		}
		return leaf.CheckSignatureFrom(signer.cert)
	})
	sc.Step(`^it is valid for 24 hours$`, func() error {
		leaf := w.tunnelCerts[len(w.tunnelCerts)-1]
		if left := time.Until(leaf.NotAfter); left < 23*time.Hour+55*time.Minute || left > 24*time.Hour {
			return failf("the certificate is valid for another %s", left)
		}
		return nil
	})
	sc.Step(`^both tunnels show the same certificate$`, func() error {
		if len(w.tunnelCerts) != 2 || !w.tunnelCerts[0].Equal(w.tunnelCerts[1]) {
			return failf("the tunnels showed %d different certificates", len(w.tunnelCerts))
		}
		return nil
	})
	sc.Step(`^TLS inside the tunnel fails to set up$`, func() error {
		if w.tunnelErr == nil {
			return failf("TLS inside the tunnel was set up")
		}
		// The proxy sees the app's refusal a moment after the app sent it.
		return w.waitForRecords(1)
	})
	sc.Step(`^the proxy answers (\d+) with a Proxy-Status header$`, func(status int) error {
		if err := w.answered(status); err != nil {
			return err
		}
		if w.resp.Header.Get("Proxy-Status") == "" {
			return failf("the answer has no Proxy-Status")
		}
		return nil
	})

	sc.Step(`^the listen.connect cert is not an issuer certificate$`, func() error {
		w.problem = "not an issuer"
		leaf := w.issuer.leaf("not-an-issuer", time.Now().Add(24*time.Hour), false)
		w.writeFile("ca/cert.pem", certPEM(leaf))
		w.writeFile("ca/key.pem", keyPEM(leaf))
		return nil
	})
	sc.Step(`^the listen.connect key does not belong to its certificate$`, func() error {
		w.problem = "not a matching certificate and key"
		w.writeFile("ca/key.pem", issuerKeyPEM(newIssuer()))
		return nil
	})
	sc.Step(`^the listen.connect issuer has already expired$`, func() error {
		w.problem = "expired"
		w.useInterceptionIssuer(newIssuerValid(time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)))
		return nil
	})
	sc.Step(`^listen.connect has a cert file but no key file$`, func() error {
		w.problem = "issuer customer needs both cert and key"
		w.listenConnect = "  connect: { issuer: customer }\n"
		w.issuersSection = "issuers:\n  customer: { cert: ./ca/cert.pem }\n"
		return nil
	})
	sc.Step(`^listen.connect names the issuer "([^"]*)", which is not in issuers$`, func(name string) error {
		w.problem = "issuer " + name + ", which is not in issuers"
		w.useInterceptionIssuer(newIssuer())
		w.listenConnect = "  connect: { issuer: " + name + " }\n"
		return nil
	})
	sc.Step(`^listen.connect has the files of an issuer and no issuer name$`, func() error {
		w.problem = "listen.connect must name an issuer"
		w.useInterceptionIssuer(newIssuer())
		w.listenConnect = "  connect: { cert: ./ca/cert.pem, key: ./ca/key.pem }\n"
		return nil
	})
	sc.Step(`^listen.connect names no issuer$`, func() error {
		w.problem = "listen.connect must name an issuer"
		w.useInterceptionIssuer(newIssuer())
		w.listenConnect = "  connect: {}\n"
		return nil
	})
	sc.Step(`^an issuer that listen.connect does not use is not an issuer certificate$`, func() error {
		w.problem = "not an issuer"
		w.useInterceptionIssuer(newIssuer())
		leaf := w.issuer.leaf("not-an-issuer", time.Now().Add(24*time.Hour), false)
		w.writeFile("ca/other-cert.pem", certPEM(leaf))
		w.writeFile("ca/other-key.pem", keyPEM(leaf))
		w.issuersSection += "  unused: { cert: ./ca/other-cert.pem, key: ./ca/other-key.pem }\n"
		return nil
	})
	sc.Step(`^the proxy logs a warning that the interception issuer is valid for more than a year$`, func() error {
		for _, line := range strings.Split(w.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, "valid for more than a year") {
				return nil
			}
		}
		return failf("no warning about the issuer; the log is %s", w.logs.String())
	})
}

// The signed call: a request whose Host, path, query, body and signed
// headers a vendor checks against the signature the app made.
const (
	signedPath = "/v2/customer_sessions/sess%2D1?b=2&a=1"
	signedBody = `{"QueueUrl":"https://sqs.example/1/q","MessageBody":"hi"}`
)

func signedHeaders() []string {
	return []string{
		"Host: api.talon.one",
		"Content-Type: application/x-amz-json-1.0",
		"X-Amz-Target: AmazonSQS.SendMessage",
		"X-Amz-Date: 20261029T120000Z",
		"X-Amz-Content-Sha256: 3f0a5d6e",
		"Authorization: AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261029/eu-west-1/sqs/aws4_request, " +
			"SignedHeaders=content-type;host;x-amz-date;x-amz-target, Signature=5d672d79",
	}
}

// useInterceptionIssuer writes an issuer to the listen.connect files.
func (w *world) useInterceptionIssuer(i *issuer) {
	w.interception = i
	w.writeFile("ca/cert.pem", issuerCertPEM(i))
	w.writeFile("ca/key.pem", issuerKeyPEM(i))
	w.listenConnect = "  connect: { issuer: customer }\n"
	w.issuersSection = "issuers:\n  customer: { cert: ./ca/cert.pem, key: ./ca/key.pem }\n"
}

// callThroughProxy makes the app's call the way an app with HTTPS_PROXY
// set does: Go's own client opens the tunnel and TLS inside it. The proxy
// address uses the scheme given, and carries the ID as its user name.
func (w *world) callThroughProxy(proxyScheme, host, id string) error {
	if err := w.start(); err != nil {
		return err
	}
	if !strings.HasPrefix(host, "10.") {
		w.vendor(host)
	}
	proxyURL := &url.URL{Scheme: proxyScheme, Host: "hotline-proxy:8443"}
	if id != "" {
		proxyURL.User = url.UserPassword(id, "x")
	}
	roots := w.tunnelRoots.Clone()
	roots.AddCert(w.issuer.cert)
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, w.addr)
		},
		TLSClientConfig:   &tls.Config{RootCAs: roots, NextProtos: w.tunnelProtos, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: !slices.Equal(w.tunnelProtos, []string{"http/1.1"}),
	}
	defer transport.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut,
		"https://"+host+"/v2/customer_sessions/sess-8812?dry=true", strings.NewReader(`{"profileId":"p-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shop", "shop-a")
	resp, err := (&http.Client{Transport: transport, Timeout: 10 * time.Second}).Do(req)
	w.resp, w.respErr = resp, err
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	w.respBody, w.respErr = io.ReadAll(resp.Body)
	return nil
}

// openTunnel sends CONNECT host:port, with the ID when one is given, and
// sets up TLS inside the tunnel, except on port 80, where the app talks
// plain HTTP. The tunnel stays open for later calls; tunnelErr says why
// TLS failed.
func (w *world) openTunnel(host, port, id string) error {
	if err := w.start(); err != nil {
		return err
	}
	w.vendor(host)
	conn, err := w.dialProxy()
	if err != nil {
		return errorsJoinTLS(err, w.tlsErr)
	}
	w.tunnels = append(w.tunnels, conn)
	head := fmt.Sprintf("CONNECT %s:%s HTTP/1.1\r\nHost: %s:%s\r\n", host, port, host, port)
	if id != "" {
		head += basic(id+":x") + "\r\n"
	}
	if _, err := io.WriteString(conn, head+"\r\n"); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	_ = conn.SetReadDeadline(time.Time{})
	if resp.StatusCode != http.StatusOK {
		return failf("the proxy answered CONNECT with %d", resp.StatusCode)
	}
	w.secureTunnel(conn, host, port)
	return nil
}

// openTunnelHTTP2 opens a tunnel the way it is done over HTTP/2: CONNECT
// as one stream of a connection to the proxy, shared by every tunnel the
// app opens this way. The app writes into the stream's request body and
// reads its reply.
func (w *world) openTunnelHTTP2(host, port, id string) error {
	if err := w.start(); err != nil {
		return err
	}
	w.vendor(host)
	if w.h2 == nil {
		protocols := new(http.Protocols)
		protocols.SetUnencryptedHTTP2(true)
		w.h2 = &http.Transport{
			Protocols: protocols,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, w.addr)
			},
		}
	}
	body, app := io.Pipe()
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { w.h2Reused = info.Reused },
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodConnect, "http://"+net.JoinHostPort(host, port), body)
	if id != "" {
		req.Header.Set("Proxy-Authorization", strings.TrimPrefix(basic(id+":x"), "Proxy-Authorization: "))
	}
	resp, err := w.h2.RoundTrip(req) //nolint:bodyclose // the reply is the tunnel, closed with it
	if err != nil {
		return err
	}
	conn := &streamConn{reply: resp.Body, body: app}
	w.tunnels = append(w.tunnels, conn)
	if resp.StatusCode != http.StatusOK {
		return failf("the proxy answered CONNECT with %d", resp.StatusCode)
	}
	w.secureTunnel(conn, host, port)
	return nil
}

// secureTunnel sets up TLS inside an open tunnel, except on port 80, where
// the app talks plain HTTP. A failed setup is what some scenarios check;
// tunnelErr says why.
func (w *world) secureTunnel(conn net.Conn, host, port string) {
	if port == "80" {
		w.tunnel = conn
		return
	}
	client := tls.Client(conn, &tls.Config{RootCAs: w.tunnelRoots, ServerName: host, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	w.tunnelErr = client.HandshakeContext(context.Background())
	_ = client.SetDeadline(time.Time{})
	if state := client.ConnectionState(); state.HandshakeComplete {
		w.tunnelCerts = append(w.tunnelCerts, state.PeerCertificates[0])
		w.tunnel = client
	}
}

// streamConn is the app's end of a tunnel opened over HTTP/2.
// A read deadline that passes ends the reply, as a stream has none.
type streamConn struct {
	reply io.ReadCloser
	body  *io.PipeWriter
	timer *time.Timer
}

func (c *streamConn) Read(p []byte) (int, error)  { return c.reply.Read(p) }
func (c *streamConn) Write(p []byte) (int, error) { return c.body.Write(p) }
func (c *streamConn) Close() error {
	return errors.Join(c.body.Close(), c.reply.Close())
}
func (c *streamConn) LocalAddr() net.Addr           { return &net.TCPAddr{} }
func (c *streamConn) RemoteAddr() net.Addr          { return &net.TCPAddr{} }
func (c *streamConn) SetDeadline(t time.Time) error { return c.SetReadDeadline(t) }
func (c *streamConn) SetReadDeadline(t time.Time) error {
	if c.timer != nil {
		c.timer.Stop()
	}
	if !t.IsZero() {
		c.timer = time.AfterFunc(time.Until(t), func() { _ = c.reply.Close() })
	}
	return nil
}
func (c *streamConn) SetWriteDeadline(time.Time) error { return nil }

// callInTunnel sends the world's usual call, with a JSON body, inside the
// open tunnel.
func (w *world) callInTunnel(headers ...string) error {
	if w.tunnel == nil {
		return failf("no tunnel is open: %v", w.tunnelErr)
	}
	body := `{"profileId":"p-1"}`
	return w.exchange(w.tunnel, request{
		method:  "PUT",
		target:  "/v2/customer_sessions/sess-8812?dry=true",
		headers: append([]string{"Content-Type: application/json"}, headers...),
		body:    strings.NewReader(body),
		length:  len(body),
	})
}

// newIssuerValid is a test certificate authority valid between two times.
func newIssuerValid(notBefore, notAfter time.Time) *issuer {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "hotline interception test issuer"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &issuer{cert: cert, key: key, pool: pool}
}

func issuerCertPEM(i *issuer) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: i.cert.Raw}))
}

func issuerKeyPEM(i *issuer) string {
	der, _ := x509.MarshalPKCS8PrivateKey(i.key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}
