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
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

func connectSteps(sc *godog.ScenarioContext, sut *connectSUT) {
	sc.Step(`^an interception certificate authority named customer in certs, which the connect part picks$`, func() error {
		sut.useInterceptionAuthority(newAuthority())
		return nil
	})
	sc.Step(`^an interception certificate authority valid for two years$`, func() error {
		sut.useInterceptionAuthority(newAuthorityValid(time.Now().Add(-time.Hour), time.Now().Add(2*365*24*time.Hour)))
		return nil
	})
	sc.Step(`^an app that trusts the interception certificate authority$`, func() error {
		sut.app.tunnelRoots = x509.NewCertPool()
		sut.app.tunnelRoots.AddCert(sut.certs.interception.cert)
		return nil
	})
	sc.Step(`^an app that does not trust the interception certificate authority$`, func() error {
		sut.app.tunnelRoots = x509.NewCertPool()
		return nil
	})
	sc.Step(`^the app offers only (HTTP/1.1|HTTP/2) inside the tunnel$`, func(version string) error {
		sut.app.tunnelProtos = []string{map[string]string{"HTTP/1.1": "http/1.1", "HTTP/2": "h2"}[version]}
		return nil
	})

	sc.Step(`^the app calls https://(\S+) through the proxy as integration (\S+)$`, func(host, id string) error {
		return sut.callThroughProxy("http", host, id)
	})
	sc.Step(`^the app calls https://(\S+) through the proxy with no ID$`, func(host string) error {
		return sut.callThroughProxy("http", host, "")
	})
	sc.Step(`^the app calls https://(\S+) through an https proxy address as integration (\S+)$`, func(host, id string) error {
		return sut.callThroughProxy("https", host, id)
	})
	sc.Step(`^the app (?:opens|has opened) a tunnel to (\S+):(\d+)$`, func(host, port string) error {
		return sut.openTunnel(host, port, "")
	})
	sc.Step(`^the app opens a tunnel to (\S+):(\d+) as integration (\S+)$`, sut.openTunnel)
	sc.Step(`^the app (?:opens|has opened) a tunnel to (\S+):(\d+) as integration (\S+) over HTTP/2$`, sut.openTunnelHTTP2)
	sc.Step(`^the app has opened a tunnel to (\S+):(\d+) over (HTTP/1.1|HTTP/2) and sent nothing in it$`, func(host, port, version string) error {
		if version == "HTTP/2" {
			return sut.openTunnelHTTP2(host, port, "")
		}
		return sut.openTunnel(host, port, "")
	})
	sc.Step(`^the app opens two tunnels to (\S+):(\d+) over one HTTP/2 connection$`, func(host, port string) error {
		for range 2 {
			if err := sut.openTunnelHTTP2(host, port, ""); err != nil {
				return err
			}
		}
		if !sut.app.h2Reused {
			return failf("the second tunnel opened its own connection")
		}
		return nil
	})
	sc.Step(`^an app sends "CONNECT (\S+)" over plain HTTP/2$`, func(authority string) error {
		return sut.app.sendHTTP2(http.MethodConnect, "", authority)
	})
	sc.Step(`^the proxy stops(?:, with (\d+) seconds? to stop)?$`, func(seconds string) error {
		if seconds != "" {
			n, _ := strconv.Atoi(seconds)
			sut.proxy.stopTime = time.Duration(n) * time.Second
		}
		stopped := make(chan struct{})
		go func() {
			sut.proxy.settle()
			close(stopped)
		}()
		select {
		case <-stopped:
			return nil
		case <-time.After(2 * time.Second):
			return failf("the proxy took over 2 seconds to stop")
		}
	})
	sc.Step(`^the proxy starts stopping$`, sut.proxy.startStopping)
	sc.Step(`^the tunnel is closed$`, func() error {
		_ = sut.app.tunnel.SetReadDeadline(time.Now().Add(2 * time.Second))
		if n, err := sut.app.tunnel.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
			return failf("the tunnel gave %d bytes and %v, want it closed", n, err)
		}
		return nil
	})
	sc.Step(`^the app opens two tunnels to (\S+):(\d+)$`, func(host, port string) error {
		for range 2 {
			if err := sut.openTunnel(host, port, ""); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^sends a call inside it with Proxy-Authorization for (\S+)$`, func(id string) error {
		return sut.app.callInTunnel("Host: api.talon.one", basic(id+":x"))
	})
	sc.Step(`^sends a call inside it with the header "([^"]+)"$`, func(header string) error {
		return sut.app.callInTunnel(header)
	})
	sc.Step(`^sends a plain HTTP call inside it$`, func() error {
		return sut.app.callInTunnel("Host: api.talon.one")
	})
	sc.Step(`^the app sends two calls through one tunnel$`, func() error {
		if err := sut.openTunnel("api.talon.one", "443", "talon-shop-a"); err != nil {
			return err
		}
		for range 2 {
			if err := sut.app.callInTunnel("Host: api.talon.one"); err != nil {
				return err
			}
			if err := sut.otlp.waitForRecords(len(sut.otlp.sentRecords) + 1); err != nil {
				return err
			}
			records := sut.otlp.receivers[0].got()
			sut.otlp.sentRecords = records
		}
		return nil
	})
	sc.Step(`^the app sends two calls on one TLS connection$`, func() error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		sut.vendors.vendor("api.talon.one")
		conn, err := sut.app.dialProxy()
		if err != nil {
			return errorsJoinTLS(err, sut.app.tlsErr)
		}
		sut.app.openConn = conn
		for range 2 {
			if err := sut.tlsIngress.callOn(conn); err != nil {
				return err
			}
			if err := sut.otlp.waitForRecords(len(sut.otlp.sentRecords) + 1); err != nil {
				return err
			}
			records := sut.otlp.receivers[0].got()
			sut.otlp.sentRecords = records
		}
		return nil
	})
	sc.Step(`^the app sends a signed call to https://api.talon.one through a tunnel$`, func() error {
		if err := sut.openTunnel("api.talon.one", "443", "talon-shop-a"); err != nil {
			return err
		}
		return sut.app.exchange(sut.app.tunnel, request{
			method:  "POST",
			target:  signedPath,
			headers: signedHeaders(),
			body:    strings.NewReader(signedBody),
			length:  len(signedBody),
		})
	})
	sc.Step(`^the files of the certificate authority named customer are replaced with a second certificate authority$`, func() error {
		second := newAuthority()
		sut.proxy.replaceFile("ca/cert.pem", authorityCertPEM(second))
		sut.proxy.replaceFile("ca/key.pem", authorityKeyPEM(second))
		sut.app.tunnelRoots.AddCert(second.cert)
		sut.certs.second = second
		return nil
	})

	sc.Step(`^the test vendor got the call over https with its Host, path, query, headers and body unchanged$`, func() error {
		c, err := sut.vendors.onlyCall("api.talon.one")
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
		c, err := sut.vendors.onlyCall("api.talon.one")
		if err != nil {
			return err
		}
		if got := c.header.Values("Proxy-Authorization"); len(got) > 0 {
			return failf("the vendor got Proxy-Authorization %v", got)
		}
		return nil
	})
	sc.Step(`^the test vendor got the same Host, path, query, body and signed headers the app signed$`, func() error {
		c, err := sut.vendors.onlyCall("api.talon.one")
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
		_, err := sut.vendors.onlyCall(host)
		return err
	})
	sc.Step(`^the test vendor got the call over https on port 443$`, func() error {
		c, err := sut.vendors.onlyCall("api.talon.one")
		if err != nil {
			return err
		}
		if c.scheme != "https" || c.port != "443" {
			return failf("the vendor got the call over %s on port %s", c.scheme, c.port)
		}
		return nil
	})
	sc.Step(`^the record says hotline.ingress (\S+) and ID (\S+)$`, func(ingress, id string) error {
		if err := sut.otlp.lastHas("hotline.ingress", ingress); err != nil {
			return err
		}
		return sut.otlp.lastHas("hotline.integration.id", id)
	})
	sc.Step(`^the record has url.template (\S+)$`, func(template string) error {
		return sut.otlp.lastHas("url.template", template)
	})
	sc.Step(`^the call inside the tunnel used (HTTP/1.1|HTTP/2)$`, func(version string) error {
		want := map[string]string{"HTTP/1.1": "HTTP/1.1", "HTTP/2": "HTTP/2.0"}[version]
		if sut.app.resp == nil || sut.app.resp.Proto != want {
			return failf("the app got %v, want %s", sut.app.resp, want)
		}
		return nil
	})
	sc.Step(`^the app sees a certificate for (\S+) signed by the (interception|second) certificate authority$`, func(host, which string) error {
		signer := sut.certs.interception
		if which == "second" {
			signer = sut.certs.second
		}
		if sut.app.tunnelErr != nil {
			return failf("TLS inside the tunnel failed: %v", sut.app.tunnelErr)
		}
		leaf := sut.app.tunnelCerts[len(sut.app.tunnelCerts)-1]
		if !slices.Contains(leaf.DNSNames, host) {
			return failf("the certificate names %v, want %s", leaf.DNSNames, host)
		}
		return leaf.CheckSignatureFrom(signer.cert)
	})
	sc.Step(`^it is valid for 24 hours$`, func() error {
		leaf := sut.app.tunnelCerts[len(sut.app.tunnelCerts)-1]
		if left := time.Until(leaf.NotAfter); left < 23*time.Hour+55*time.Minute || left > 24*time.Hour {
			return failf("the certificate is valid for another %s", left)
		}
		return nil
	})
	sc.Step(`^both tunnels show the same certificate$`, func() error {
		if len(sut.app.tunnelCerts) != 2 || !sut.app.tunnelCerts[0].Equal(sut.app.tunnelCerts[1]) {
			return failf("the tunnels showed %d different certificates", len(sut.app.tunnelCerts))
		}
		return nil
	})
	sc.Step(`^TLS inside the tunnel fails to set up$`, func() error {
		if sut.app.tunnelErr == nil {
			return failf("TLS inside the tunnel was set up")
		}
		// The proxy sees the app's refusal a moment after the app sent it.
		return sut.otlp.waitForRecords(1)
	})
	sc.Step(`^the proxy answers (\d+) with a Proxy-Status header$`, func(status int) error {
		if err := sut.app.answered(status); err != nil {
			return err
		}
		if sut.app.resp.Header.Get("Proxy-Status") == "" {
			return failf("the answer has no Proxy-Status")
		}
		return nil
	})

	sc.Step(`^the connect part cert is not a certificate authority's certificate$`, func() error {
		sut.proxy.problem = "is not a certificate authority, so it cannot sign"
		leaf := sut.vendors.authority.leaf("not-a-certificate-authority", time.Now().Add(24*time.Hour), false)
		sut.proxy.writeFile("ca/cert.pem", certPEM(leaf))
		sut.proxy.writeFile("ca/key.pem", keyPEM(leaf))
		return nil
	})
	sc.Step(`^the connect part key does not belong to its certificate$`, func() error {
		sut.proxy.problem = "not a matching certificate and key"
		sut.proxy.writeFile("ca/key.pem", authorityKeyPEM(newAuthority()))
		return nil
	})
	sc.Step(`^the connect part certificate authority has already expired$`, func() error {
		sut.proxy.problem = "expired"
		sut.useInterceptionAuthority(newAuthorityValid(time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)))
		return nil
	})
	sc.Step(`^the connect part has a cert file but no key file$`, func() error {
		sut.proxy.problem = "cert customer needs both cert and key"
		sut.listenConnect = "  connect: { cert: customer }\n"
		sut.certs.certEntries = "certs:\n  customer: { cert: ./ca/cert.pem }\n"
		return nil
	})
	sc.Step(`^the connect part names the cert "([^"]*)", which is not in certs$`, func(name string) error {
		sut.proxy.problem = "names the cert " + name + ", which is not in certs"
		sut.useInterceptionAuthority(newAuthority())
		sut.listenConnect = "  connect: { cert: " + name + " }\n"
		return nil
	})
	sc.Step(`^a certificate authority is written with no files$`, func() error {
		sut.proxy.problem = "cert customer needs both cert and key"
		sut.useInterceptionAuthority(newAuthority())
		sut.certs.certEntries = "certs:\n  customer:\n"
		return nil
	})
	sc.Step(`^the connect part names no cert$`, func() error {
		sut.proxy.problem = "connect needs a cert, as cert: name, on it or on the listener"
		sut.useInterceptionAuthority(newAuthority())
		sut.listenConnect = "  connect: {}\n"
		return nil
	})
	sc.Step(`^a cert that nothing signs with is not a certificate authority$`, func() error {
		sut.useInterceptionAuthority(newAuthority())
		leaf := sut.vendors.authority.leaf("not-a-certificate-authority", time.Now().Add(24*time.Hour), false)
		sut.proxy.writeFile("ca/other-cert.pem", certPEM(leaf))
		sut.proxy.writeFile("ca/other-key.pem", keyPEM(leaf))
		sut.certs.certEntries += "  unused: { cert: ./ca/other-cert.pem, key: ./ca/other-key.pem }\n"
		return nil
	})
	sc.Step(`^the proxy logs a warning that the interception certificate authority is valid for more than a year$`, func() error {
		for _, line := range strings.Split(sut.proxy.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, "valid for more than a year") {
				return nil
			}
		}
		return failf("no warning about the certificate authority; the log is %s", sut.proxy.logs.String())
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

// useInterceptionAuthority writes a certificate authority to the connect part files.
func (sut *connectSUT) useInterceptionAuthority(i *authority) {
	sut.certs.useAuthority(i)
	sut.listenConnect = "  connect: { cert: customer }\n"
}

// useAuthority writes a certificate authority to the files of the certificate authority named customer.
func (sut *certsSUT) useAuthority(i *authority) {
	sut.interception = i
	sut.proxy.writeFile("ca/cert.pem", authorityCertPEM(i))
	sut.proxy.writeFile("ca/key.pem", authorityKeyPEM(i))
	sut.certEntries = "certs:\n  customer: { cert: ./ca/cert.pem, key: ./ca/key.pem }\n"
}

// callThroughProxy makes the app's call the way an app with HTTPS_PROXY
// set does: Go's own client opens the tunnel and TLS inside it. The proxy
// address uses the scheme given, and carries the ID as its user name.
func (sut *connectSUT) callThroughProxy(proxyScheme, host, id string) error {
	if err := sut.proxy.start(); err != nil {
		return err
	}
	if !strings.HasPrefix(host, "10.") {
		sut.vendors.vendor(host)
	}
	proxyURL := &url.URL{Scheme: proxyScheme, Host: "hotline-proxy:8443"}
	if id != "" {
		proxyURL.User = url.UserPassword(id, "x")
	}
	roots := sut.app.tunnelRoots.Clone()
	roots.AddCert(sut.vendors.authority.cert)
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, sut.proxy.addr)
		},
		TLSClientConfig:   &tls.Config{RootCAs: roots, NextProtos: sut.app.tunnelProtos, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: !slices.Equal(sut.app.tunnelProtos, []string{"http/1.1"}),
	}
	defer transport.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut,
		"https://"+host+"/v2/customer_sessions/sess-8812?dry=true", strings.NewReader(`{"profileId":"p-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shop", "shop-a")
	resp, err := (&http.Client{Transport: transport, Timeout: 10 * time.Second}).Do(req)
	sut.app.resp, sut.app.respErr = resp, err
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	sut.app.respBody, sut.app.respErr = io.ReadAll(resp.Body)
	return nil
}

// openTunnel sends CONNECT host:port, with the ID when one is given, and
// sets up TLS inside the tunnel, except on port 80, where the app talks
// plain HTTP. The tunnel stays open for later calls; tunnelErr says why
// TLS failed.
func (sut *connectSUT) openTunnel(host, port, id string) error {
	if err := sut.proxy.start(); err != nil {
		return err
	}
	sut.vendors.vendor(host)
	conn, err := sut.app.dialProxy()
	if err != nil {
		return errorsJoinTLS(err, sut.app.tlsErr)
	}
	sut.app.tunnels = append(sut.app.tunnels, conn)
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
	sut.app.secureTunnel(conn, host, port)
	return nil
}

// openTunnelHTTP2 opens a tunnel the way it is done over HTTP/2: CONNECT
// as one stream of a connection to the proxy, shared by every tunnel the
// app opens this way. The app writes into the stream's request body and
// reads its response.
func (sut *connectSUT) openTunnelHTTP2(host, port, id string) error {
	if err := sut.proxy.start(); err != nil {
		return err
	}
	sut.vendors.vendor(host)
	if sut.app.h2 == nil {
		protocols := new(http.Protocols)
		protocols.SetUnencryptedHTTP2(true)
		sut.app.h2 = &http.Transport{
			Protocols: protocols,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, sut.proxy.addr)
			},
		}
	}
	body, app := io.Pipe()
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { sut.app.h2Reused = info.Reused },
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodConnect, "http://"+net.JoinHostPort(host, port), body)
	if id != "" {
		req.Header.Set("Proxy-Authorization", strings.TrimPrefix(basic(id+":x"), "Proxy-Authorization: "))
	}
	resp, err := sut.app.h2.RoundTrip(req) //nolint:bodyclose // the response is the tunnel, closed with it
	if err != nil {
		return err
	}
	conn := &streamConn{response: resp.Body, body: app}
	sut.app.tunnels = append(sut.app.tunnels, conn)
	if resp.StatusCode != http.StatusOK {
		return failf("the proxy answered CONNECT with %d", resp.StatusCode)
	}
	sut.app.secureTunnel(conn, host, port)
	return nil
}

// secureTunnel sets up TLS inside an open tunnel, except on port 80, where
// the app talks plain HTTP. A failed setup is what some scenarios check;
// tunnelErr says why.
func (sut *appSUT) secureTunnel(conn net.Conn, host, port string) {
	if port == "80" {
		sut.tunnel = conn
		return
	}
	client := tls.Client(conn, &tls.Config{RootCAs: sut.tunnelRoots, ServerName: host, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	sut.tunnelErr = client.HandshakeContext(context.Background())
	_ = client.SetDeadline(time.Time{})
	if state := client.ConnectionState(); state.HandshakeComplete {
		sut.tunnelCerts = append(sut.tunnelCerts, state.PeerCertificates[0])
		sut.tunnel = client
	}
}

// streamConn is the app's end of a tunnel opened over HTTP/2.
// A read deadline that passes ends the response, as a stream has none.
type streamConn struct {
	response io.ReadCloser
	body     *io.PipeWriter
	timer    *time.Timer
}

func (c *streamConn) Read(p []byte) (int, error)  { return c.response.Read(p) }
func (c *streamConn) Write(p []byte) (int, error) { return c.body.Write(p) }
func (c *streamConn) Close() error {
	return errors.Join(c.body.Close(), c.response.Close())
}

// CloseWrite ends the request body, which tells the proxy the app sends
// nothing more, and keeps reading the response.
func (c *streamConn) CloseWrite() error             { return c.body.Close() }
func (c *streamConn) LocalAddr() net.Addr           { return &net.TCPAddr{} }
func (c *streamConn) RemoteAddr() net.Addr          { return &net.TCPAddr{} }
func (c *streamConn) SetDeadline(t time.Time) error { return c.SetReadDeadline(t) }
func (c *streamConn) SetReadDeadline(t time.Time) error {
	if c.timer != nil {
		c.timer.Stop()
	}
	if !t.IsZero() {
		c.timer = time.AfterFunc(time.Until(t), func() { _ = c.response.Close() })
	}
	return nil
}
func (c *streamConn) SetWriteDeadline(time.Time) error { return nil }

// callInTunnel sends the world's usual call, with a JSON body, inside the
// open tunnel.
func (sut *appSUT) callInTunnel(headers ...string) error {
	if sut.tunnel == nil {
		return failf("no tunnel is open: %v", sut.tunnelErr)
	}
	body := `{"profileId":"p-1"}`
	return sut.exchange(sut.tunnel, request{
		method:  "PUT",
		target:  "/v2/customer_sessions/sess-8812?dry=true",
		headers: append([]string{"Content-Type: application/json"}, headers...),
		body:    strings.NewReader(body),
		length:  len(body),
	})
}

// newAuthorityValid is a test certificate authority valid between two times.
func newAuthorityValid(notBefore, notAfter time.Time) *authority {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "hotline interception test certificate authority"},
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
	return &authority{cert: cert, key: key, pool: pool}
}

func authorityCertPEM(i *authority) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: i.cert.Raw}))
}

func authorityKeyPEM(i *authority) string {
	der, _ := x509.MarshalPKCS8PrivateKey(i.key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// connectSUT is the CONNECT ingress: apps that open a tunnel through the
// proxy port, and the proxy reading the calls inside it.
type connectSUT struct {
	proxy      *proxySUT
	vendors    *vendorsSUT
	certs      *certsSUT
	app        *appSUT
	otlp       *otlpSUT
	tlsIngress *tlsIngressSUT
	// listenConnect, when set, is the connect line of the proxy listener.
	listenConnect string
}

func newConnectSUT(root *proxySUT, vendors *vendorsSUT, certs *certsSUT, app *appSUT, otlp *otlpSUT, tlsIngress *tlsIngressSUT) *connectSUT {
	sut := &connectSUT{proxy: root, vendors: vendors, certs: certs, app: app, otlp: otlp, tlsIngress: tlsIngress}
	root.addConfig(configProxyListener, func() string { return sut.listenConnect })
	return sut
}

// AddSteps adds the steps of the CONNECT ingress.
func (sut *connectSUT) AddSteps(sc *godog.ScenarioContext) {
	connectSteps(sc, sut)
}
