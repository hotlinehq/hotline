package proxy

import (
	"bufio"
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"hotline-proxy/internal/capture"
)

// captureWriteTimeout is how long a send of the app list to the extension may
// take; an extension that does not read must not hold up a config reload.
const captureWriteTimeout = 5 * time.Second

// localApp is a connection from the extension, which comes from a program on
// this machine: its address is the machine's own.
type localApp struct {
	net.Conn
}

func (localApp) RemoteAddr() net.Addr {
	return net.TCPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0"))
}

// CloseWrite says nothing more comes, when the connection can say that.
func (c localApp) CloseWrite() error {
	if w, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return w.CloseWrite()
	}
	return c.Close()
}

// captureHub holds the extension's control connection and the app list it
// was last sent.
type captureHub struct {
	mu       sync.Mutex
	list     []byte
	controls map[net.Conn]struct{}
	closed   bool
}

func newCaptureHub(apps []string) *captureHub {
	h := &captureHub{controls: map[net.Conn]struct{}{}}
	h.update(apps)
	return h
}

// update makes the app list apps the one to send, and sends it to every
// control connection.
func (h *captureHub) update(apps []string) {
	list, err := capture.Frame(capture.Spec(apps, os.Getpid()))
	if err != nil {
		// The config check keeps entries small; a list too large to send is
		// not sent, and the extension keeps the old one.
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.list = list
	for conn := range h.controls {
		_ = conn.SetWriteDeadline(time.Now().Add(captureWriteTimeout))
		if _, err := conn.Write(list); err != nil {
			_ = conn.Close()
		}
	}
}

// join sends the app list on a new control connection and keeps the
// connection for later lists. It says false when the hub is closed.
func (h *captureHub) join(conn net.Conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	_ = conn.SetWriteDeadline(time.Now().Add(captureWriteTimeout))
	if _, err := conn.Write(h.list); err != nil {
		return false
	}
	h.controls[conn] = struct{}{}
	return true
}

func (h *captureHub) leave(conn net.Conn) {
	h.mu.Lock()
	delete(h.controls, conn)
	h.mu.Unlock()
}

// close ends every control connection.
func (h *captureHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for conn := range h.controls {
		_ = conn.Close()
	}
}

// serveCapture takes the connections the macOS extension opens on the capture
// socket, until it is closed.
func (h *handler) serveCapture(l net.Listener, hub *captureHub) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		h.calls.Add(1)
		go func() {
			defer h.calls.Done()
			h.captured(localApp{Conn: conn}, hub)
		}()
	}
}

// captured handles one connection from the extension: its first message
// says whether it is the control connection, which gets the app list, or a
// captured flow, which is handled like a connection on the intercept
// listener, with the destination and program from the hello.
func (h *handler) captured(conn localApp, hub *captureHub) {
	start := h.clock.Now()
	cc := h.cfg.Load().Capture
	if cc == nil {
		_ = conn.Close()
		return
	}
	src := &source{ingress: "capture", id: cc.Integration, passes: cc.Passes, relays: cc.Relays, certificate: h.captureCerts}
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	reader := bufio.NewReaderSize(conn, maxHello)
	hello, err := capture.ReadHello(reader)
	if err != nil {
		h.logger.Debug("a connection on the capture socket sent no usable hello", "reason", err.Error())
		h.interceptRecord(src, conn, start, "", semconv.ErrorTypeKey.String("capture_bad_hello"))
		_ = conn.Close()
		return
	}
	if hello.Role == capture.RoleControl {
		h.control(conn, reader, hub)
		return
	}
	src.port = strconv.Itoa(hello.Port)
	src.fallback = strings.ToLower(hello.Host)
	if hello.PID > 0 {
		src.extra = append(src.extra, semconv.ProcessPID(hello.PID))
	}
	if hello.ProcessName != "" {
		src.extra = append(src.extra, semconv.ProcessExecutablePath(hello.ProcessName))
	}
	h.serveSource(conn, reader, src, start)
}

// control keeps the extension's control connection until either side ends
// it. Nothing more is expected from the extension on it.
func (h *handler) control(conn localApp, reader *bufio.Reader, hub *captureHub) {
	_ = conn.SetDeadline(time.Time{})
	if !hub.join(conn) {
		_ = conn.Close()
		return
	}
	defer hub.leave(conn)
	defer func() { _ = conn.Close() }()
	buf := make([]byte, 512)
	for {
		if _, err := reader.Read(buf); err != nil {
			if !errors.Is(err, net.ErrClosed) {
				h.logger.Debug("the capture control connection ended", "reason", err.Error())
			}
			return
		}
	}
}
