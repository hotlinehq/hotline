package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"hotline-proxy/internal/config"
)

// The headers the macOS capture extension adds to its CONNECT
// (hotline-macos-capture, docs/protocol.md). captureVersion is the one
// version the proxy knows.
const (
	captureHeader        = "Hotline-Capture"
	capturePIDHeader     = "Hotline-Capture-Pid"
	captureProgramHeader = "Hotline-Capture-Program"
	// maxProgram is the longest program path a record takes; the
	// extension sends none longer.
	maxProgram = 4096
	// captureVersion 2 goes over listen.capture; 1 went to the proxy
	// port and is refused (Peter, 2026-11-12).
	captureVersion = "2"
)

var (
	errBadCapture = errors.New("the capture headers are not usable")
	// errCaptureInUse and errNotSocket stop the proxy from starting on a
	// capture path that is not its own.
	errCaptureInUse = errors.New("listen.capture is in use by another program")
	errNotSocket    = errors.New("is not a socket file, so the proxy leaves it")
)

// listenCapture opens the socket file of listen.capture, which the
// capture extension sends its CONNECT to. A socket file left by an earlier
// run is replaced; one another program still listens on, or a file that is
// not a socket, stops the proxy. Only the proxy's own user, and root, can
// open the file.
func listenCapture(ctx context.Context, path string) (net.Listener, error) {
	// A folder that cannot be made shows as the listen failing below.
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	if conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", path); err == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %s", errCaptureInUse, path)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("listen.capture: %s %w", path, errNotSocket)
		}
		// A file that cannot be removed shows as the listen failing below.
		_ = os.Remove(path)
	}
	l, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err == nil {
		// The mode is what keeps other users out: only the proxy's own
		// user, and root, as the capture extension runs, can open the file.
		err = os.Chmod(path, 0o600)
	}
	if err != nil {
		closeAll(l)
		return nil, fmt.Errorf("listen.capture: %w", err)
	}
	return l, nil
}

// serveCapture serves a connection to listen.capture. Only a CONNECT is
// taken; its capture headers name the program behind the tunnel. Only the
// proxy's own user and root can open the socket file, so the headers are
// believed (Peter, 2026-11-12).
func (h *handler) serveCapture(w http.ResponseWriter, r *http.Request) {
	h.calls.Add(1)
	defer h.calls.Done()
	start := h.clock.Now()
	id := integrationID(r.Header)
	if r.Method != http.MethodConnect {
		refuse(w, http.StatusMethodNotAllowed)
		h.refused(r, start, "capture", id, nil, http.StatusMethodNotAllowed)
		return
	}
	t, ok := parseTunnel(r)
	if !ok {
		refuse(w, http.StatusBadRequest)
		h.refused(r, start, "capture", t.id, &url.URL{Host: t.host}, http.StatusBadRequest)
		return
	}
	h.captured(w, r, t, start)
}

// captureProcess reads the program from the capture headers. An unknown
// version, or a header given twice or out of range, is refused.
func captureProcess(h http.Header) (process, error) {
	version, pids, programs := h.Values(captureHeader), h.Values(capturePIDHeader), h.Values(captureProgramHeader)
	if len(version) != 1 || version[0] != captureVersion || len(pids) > 1 || len(programs) > 1 {
		return process{}, errBadCapture
	}
	var p process
	if len(pids) == 1 {
		pid, err := strconv.ParseInt(pids[0], 10, 32)
		if err != nil || pid < 1 {
			return process{}, errBadCapture
		}
		p.pid = int(pid)
	}
	if len(programs) == 1 {
		if len(programs[0]) > maxProgram {
			return process{}, errBadCapture
		}
		p.path = programs[0]
	}
	return p, nil
}

// captured serves a CONNECT from the capture extension. The tunnel carries
// an app's connection as the app sent it, to the address the app called, so
// it is served like an intercepted connection: the site name comes from the
// TLS handshake, with the CONNECT's host when the handshake names none.
func (h *handler) captured(w http.ResponseWriter, r *http.Request, t *tunnel, start time.Time) {
	p, err := captureProcess(r.Header)
	if err != nil {
		refuse(w, http.StatusBadRequest)
		h.refused(r, start, "capture", t.id, &url.URL{Host: t.host}, http.StatusBadRequest)
		return
	}
	// A tunnel has no list of ECH outer names to relay: an empty list relays
	// none, so a program offering ECH sees it rejected and retries without.
	src := &source{
		ingress: "capture", id: t.id, passes: h.cfg.Load().ConnectPassthrough.Passes, relays: config.Passthrough(nil).Passes,
		certificate: h.intercept, port: t.port, fallback: t.host, process: p,
	}
	conn, done := open(w, r)
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	h.serveSource(conn, bufio.NewReaderSize(conn, maxHello), src, start)
	<-done
}

// passedThrough relays an ordinary tunnel to a site listed in
// listen.connect passthrough, untouched.
func (h *handler) passedThrough(conn net.Conn, t *tunnel, start time.Time) {
	src := &source{ingress: "connect", id: t.id, port: t.port}
	h.relay(src, &bufferedConn{Conn: conn, r: conn}, start, t.host, "passthrough")
}
