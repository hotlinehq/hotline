package proxy

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
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
	maxProgram     = 4096
	captureVersion = "1"
)

var errBadCapture = errors.New("the capture headers are not usable")

// isCaptured says whether a CONNECT came from the capture extension: it
// carries the capture header and comes from this machine. From anywhere
// else the header is ignored, so no other machine can name a program.
func isCaptured(r *http.Request) bool {
	if len(r.Header.Values(captureHeader)) == 0 {
		return false
	}
	addr, err := netip.ParseAddrPort(r.RemoteAddr)
	return err == nil && addr.Addr().Unmap().IsLoopback()
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
