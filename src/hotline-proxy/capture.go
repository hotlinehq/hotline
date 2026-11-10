package proxy

import (
	"bufio"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"hotline-proxy/internal/capture"
	"hotline-proxy/internal/config"
)

// captureWriteTimeout is how long a send of the app list to the extension may
// take; an extension that does not read must not hold up a config reload.
const captureWriteTimeout = 5 * time.Second

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

// captured handles a connection on the intercept port that starts with the
// extension's framed message, which says whether it is the control
// connection, which gets the app list, or a captured flow, which is handled
// like any intercepted connection, with the destination and program from the
// hello. The first byte of the message is waiting in reader.
func (h *handler) captured(conn net.Conn, reader *bufio.Reader, ic *config.Intercept, start time.Time) {
	src := &source{ingress: "capture", id: ic.Integration, passes: ic.Passes, relays: ic.Relays, certificate: h.redirected}
	hello, err := capture.ReadHello(reader)
	if err != nil {
		h.logger.Debug("a connection on the intercept port sent no usable hello", "reason", err.Error())
		h.interceptRecord(src, conn, start, "", semconv.ErrorTypeKey.String("capture_bad_hello"))
		_ = conn.Close()
		return
	}
	if hello.Role == capture.RoleControl {
		h.control(conn, reader)
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
func (h *handler) control(conn net.Conn, reader *bufio.Reader) {
	_ = conn.SetDeadline(time.Time{})
	if !h.capture.join(conn) {
		_ = conn.Close()
		return
	}
	defer h.capture.leave(conn)
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
