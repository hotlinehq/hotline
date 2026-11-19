package proxy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"hotline-proxy/internal/config"
)

// certHolder is a certificate the proxy shows: its own, from listen.tls,
// or the certificate authority it signs certificates for vendor hosts with, from
// listen.connect. A reload puts a new one in; new connections get it, and
// connections already open keep the old one.
type certHolder struct {
	logger *slog.Logger
	// taken is the log line for a new certificate in use.
	taken   string
	current atomic.Pointer[tls.Certificate]
}

// newAppCert holds the proxy's own certificate, from listen.tls.
func newAppCert(t *config.TLS, logger *slog.Logger) *certHolder {
	return newCertHolder(t, logger, "new TLS certificate in use")
}

func newCertHolder(t *config.TLS, logger *slog.Logger, taken string) *certHolder {
	c := &certHolder{logger: logger, taken: taken}
	c.current.Store(t.Certificate)
	return c
}

// tlsConfig is the TLS setup of the proxy port: TLS 1.2 and 1.3 with Go's
// default ciphers, offering HTTP/2 and HTTP/1.1, with the same certificate
// whatever name the app asks for.
func (c *certHolder) tlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{"h2", "http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return c.get(), nil },
	}
}

// get gives the certificate for a new connection.
func (c *certHolder) get() *tls.Certificate {
	return c.current.Load()
}

// set puts in the certificate of a new config, when it is another one.
func (c *certHolder) set(t *config.TLS) {
	if bytes.Equal(c.get().Certificate[0], t.Certificate.Certificate[0]) {
		return
	}
	c.current.Store(t.Certificate)
	c.logger.Info(c.taken, "not_after", t.Certificate.Leaf.NotAfter)
}

// serverLog routes the HTTP server's own log lines to the proxy's log, as
// warnings.
type serverLog struct{ logger *slog.Logger }

func (s serverLog) Write(p []byte) (int, error) {
	s.logger.Warn("http server", "detail", strings.TrimSpace(string(p)))
	return len(p), nil
}

// errPlainHTTP is why TLS failed to set up when an app sent plain HTTP.
var errPlainHTTP = errors.New("proxy: the app sent plain HTTP to the TLS port")

// handshakeTimeout bounds how long an app may take to set up TLS.
const handshakeTimeout = 30 * time.Second

// tlsOnlyListener hands out connections on which the proxy sets up TLS
// itself: they answer plain HTTP with 400, so an app that forgot TLS
// learns why, and must start TLS within handshakeTimeout.
type tlsOnlyListener struct {
	net.Listener
	config *tls.Config
	clock  Clock
	logger *slog.Logger
	own    *own
}

func (l tlsOnlyListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	// The server sets its own read deadlines once a call starts.
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	return secureAppConn(conn, l.config, l.clock, l.logger, l.own), nil
}

// tlsOnlyConn checks the first byte an app sends: TLS always starts with a
// handshake record, 0x16.
type tlsOnlyConn struct {
	net.Conn
	checked bool
}

func (c *tlsOnlyConn) Read(p []byte) (int, error) {
	if c.checked {
		return c.Conn.Read(p)
	}
	c.checked = true
	n, err := c.Conn.Read(p)
	if n > 0 && p[0] != 0x16 {
		c.refusePlain(p[:n])
		return 0, errPlainHTTP
	}
	return n, err
}

// refusePlain reads the rest of the plain request, so closing the
// connection does not reset it before the app reads the answer, and
// answers 400.
func (c *tlsOnlyConn) refusePlain(first []byte) {
	req, err := http.ReadRequest(bufio.NewReader(io.MultiReader(bytes.NewReader(first), c.Conn)))
	if err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(req.Body, 1<<20))
	}
	const body = "the proxy port accepts only TLS\n"
	_, _ = io.WriteString(c.Conn, "HTTP/1.1 400 Bad Request\r\n"+
		"Proxy-Status: hotline; error=http_request_error\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n"+
		"Connection: close\r\n"+
		"Content-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"+body)
}
