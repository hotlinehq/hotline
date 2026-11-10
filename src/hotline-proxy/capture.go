package proxy

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"time"

	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"hotline-proxy/internal/capture"
	"hotline-proxy/internal/config"
)

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

// control hands the extension's control connection to the hub, which keeps
// it until either side ends it.
func (h *handler) control(conn net.Conn, reader *bufio.Reader) {
	if err := h.capture.Serve(conn, reader); err != nil {
		h.logger.Debug("the capture control connection ended", "reason", err.Error())
	}
}
