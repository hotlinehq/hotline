package proxy_test

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// openSocket is the app's end of a WebSocket opened through the proxy.
type openSocket struct {
	conn   net.Conn
	reader *bufio.Reader
}

func websocketSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^(the shop vendor|Gemini Live) accepts a WebSocket upgrade, reads (\d+) bytes, sends "(\w+)" and closes$`, func(who string, n int, answer string) error {
		host := map[string]string{"the shop vendor": "api.shop.test", "Gemini Live": geminiHost}[who]
		w.vendor(host).setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			conn, buf := switchToWebSocket(rw)
			if conn == nil {
				return
			}
			defer func() { _ = conn.Close() }()
			if _, err := io.ReadFull(buf, make([]byte, n)); err == nil {
				_, _ = io.WriteString(conn, answer)
			}
		})
		return nil
	})
	sc.Step(`^the shop vendor accepts a WebSocket upgrade and keeps it open until the app closes$`, func() error {
		w.vendor("api.shop.test").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			conn, buf := switchToWebSocket(rw)
			if conn == nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = io.Copy(io.Discard, buf)
		})
		return nil
	})
	sc.Step(`^an app opens a WebSocket to the shop through the (\w+) ingress$`, w.openWebSocket)
	sc.Step(`^the integration shop-assistant uses Gemini Live with the spec in specs/google$`, func() error {
		spec, err := os.ReadFile(filepath.Join("..", "..", "specs", "google", "gemini-live", "v1beta.openapi.yaml"))
		if err != nil {
			return err
		}
		w.addIntegration(integration("shop-assistant", w.specAPI(geminiHost, string(spec), "")))
		return nil
	})
	sc.Step(`^shop-assistant opens a Gemini Live session through the connect ingress with "([^"]+)"$`, func(header string) error {
		return w.openSocketTo("connect", geminiHost, geminiPath, "shop-assistant", header)
	})
	sc.Step(`^no record, span or log holds "([^"]+)"$`, func(secret string) error {
		return w.nowhere(secret)
	})
	sc.Step(`^the app sends "(\w+)" and reads "(\w+)"$`, func(message, answer string) error {
		if _, err := io.WriteString(w.socket.conn, message); err != nil {
			return err
		}
		got := make([]byte, len(answer))
		if _, err := io.ReadFull(w.socket.reader, got); err != nil || string(got) != answer {
			return failf("the app got %q, %v", got, err)
		}
		return nil
	})
	sc.Step(`^the app closes the WebSocket$`, func() error {
		return w.socket.conn.Close()
	})
	sc.Step(`^the app keeps the WebSocket open for (\d+) ms and closes it$`, func(ms int) error {
		time.Sleep(time.Duration(ms) * time.Millisecond)
		return w.socket.conn.Close()
	})
	sc.Step(`^a call record with http.response.status_code 101 arrives while the WebSocket is open$`, func() error {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if records := w.receivers[0].got(); len(records) > 0 {
				return has(records[0].attrs, "http.response.status_code", 101)
			}
		}
		return failf("no call record arrived while the WebSocket was open")
	})
	sc.Step(`^no session record has arrived yet$`, func() error {
		if sessions := w.receivers[0].sessionRecords(); len(sessions) > 0 {
			return failf("a session record arrived while the WebSocket was open: %v", sessions[0].attrs)
		}
		return nil
	})
	sc.Step(`^there is one call record and one session record$`, func() error {
		_, _, err := w.callAndSession()
		return err
	})
	sc.Step(`^the call record says hotline.schema.status valid and url.template (\S+)$`, func(template string) error {
		call, _, err := w.callAndSession()
		if err != nil {
			return err
		}
		return errors.Join(has(call.attrs, "hotline.schema.status", "valid"), has(call.attrs, "url.template", template))
	})
	sc.Step(`^the call record says http.response.status_code 101 and hotline.ingress (\w+)$`, func(ingress string) error {
		call, _, err := w.callAndSession()
		if err != nil {
			return err
		}
		return errors.Join(has(call.attrs, "http.response.status_code", 101), has(call.attrs, "hotline.ingress", ingress))
	})
	sc.Step(`^the session record says (\S+) (\S+)$`, func(key, value string) error {
		_, session, err := w.callAndSession()
		if err != nil {
			return err
		}
		return has(session.attrs, key, value)
	})
	sc.Step(`^the session record says (\S+) (\S+) and (\S+) (\S+)$`, func(key, value, key2, value2 string) error {
		_, session, err := w.callAndSession()
		if err != nil {
			return err
		}
		return errors.Join(has(session.attrs, key, value), has(session.attrs, key2, value2))
	})
	sc.Step(`^the call record says http.response.body.size 0, with the hash of no body$`, func() error {
		call, _, err := w.callAndSession()
		if err != nil {
			return err
		}
		return errors.Join(has(call.attrs, "http.response.body.size", 0),
			has(call.attrs, "hotline.response.body.hash", bodyHash("response", call.traceID, call.spanID, nil)))
	})
	sc.Step(`^the call record's hotline.timing.total_us is under (\d+) ms$`, func(ms int64) error {
		call, _, err := w.callAndSession()
		if err != nil {
			return err
		}
		if total, ok := call.attrs["hotline.timing.total_us"].(int64); !ok || total >= ms*1000 {
			return failf("the call's total time is %v µs, want under %d ms", call.attrs["hotline.timing.total_us"], ms)
		}
		return nil
	})
	sc.Step(`^the session record's hotline.websocket.duration_us is at least (\d+) ms$`, func(ms int64) error {
		_, session, err := w.callAndSession()
		if err != nil {
			return err
		}
		if open, ok := session.attrs["hotline.websocket.duration_us"].(int64); !ok || open < ms*1000 {
			return failf("the session was open %v µs, want at least %d ms", session.attrs["hotline.websocket.duration_us"], ms)
		}
		return nil
	})
	sc.Step(`^the session record's hotline.websocket.from_app.hash is the hash of "(\w+)", and its hotline.websocket.from_vendor.hash of "(\w+)"$`, func(fromApp, fromVendor string) error {
		_, s, err := w.callAndSession()
		if err != nil {
			return err
		}
		return errors.Join(
			has(s.attrs, "hotline.websocket.from_app.hash", bodyHash("from_app", s.traceID, s.spanID, []byte(fromApp))),
			has(s.attrs, "hotline.websocket.from_vendor.hash", bodyHash("from_vendor", s.traceID, s.spanID, []byte(fromVendor))))
	})
	sc.Step(`^the session record has the call record's trace_id$`, func() error {
		call, session, err := w.callAndSession()
		if err != nil {
			return err
		}
		if session.traceID != call.traceID {
			return failf("the session's trace_id is %s, the call's %s", session.traceID, call.traceID)
		}
		return nil
	})
	sc.Step(`^the session record's span is a "websocket session" span under the call's "hotline call" span, starting when the call ends$`, func() error {
		call, session, err := w.callAndSession()
		if err != nil {
			return err
		}
		var callSpan, sessionSpan *receivedSpan
		for _, s := range w.receivers[0].spans() {
			switch s.spanID {
			case call.spanID:
				callSpan = &s
			case session.spanID:
				sessionSpan = &s
			}
		}
		switch {
		case callSpan == nil || sessionSpan == nil:
			return failf("the receiver has the call span %v and the session span %v", callSpan, sessionSpan)
		case callSpan.name != "hotline call" || sessionSpan.name != "websocket session":
			return failf("the spans are named %q and %q", callSpan.name, sessionSpan.name)
		case sessionSpan.parent != callSpan.spanID || sessionSpan.traceID != callSpan.traceID:
			return failf("the session span's parent is %s, want %s", sessionSpan.parent, callSpan.spanID)
		case sessionSpan.start != callSpan.end || sessionSpan.end < sessionSpan.start:
			return failf("the session span runs %d to %d, the call span ends at %d", sessionSpan.start, sessionSpan.end, callSpan.end)
		}
		return nil
	})
	sc.Step(`^the third line is the time and "([^"]*)"$`, func(want string) error {
		return w.consoleLine(2, `^\d\d:\d\d:\d\d\.\d\d\d `+regexp.QuoteMeta(want)+`$`)
	})
	sc.Step(`^the fourth line is "([^"]*)" and the time the session was open$`, func(want string) error {
		return w.consoleLine(3, `^ *`+regexp.QuoteMeta(want)+` \d+\.\d ms$`)
	})
}

// geminiHost and geminiPath are where Google's SDKs open a Gemini Live
// session.
const (
	geminiHost = "generativelanguage.googleapis.com"
	geminiPath = "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
)

// switchToWebSocket answers 101 and takes over the connection, as a vendor's
// WebSocket server does; conn is nil when it could not.
func switchToWebSocket(rw http.ResponseWriter) (net.Conn, *bufio.ReadWriter) {
	rw.Header().Set("Connection", "Upgrade")
	rw.Header().Set("Upgrade", "websocket")
	rw.WriteHeader(http.StatusSwitchingProtocols)
	conn, buf, err := http.NewResponseController(rw).Hijack()
	if err != nil {
		return nil, nil
	}
	return conn, buf
}

// openWebSocket opens a WebSocket to the shop's /orders through the given
// ingress, as integration talon-shop-a, and keeps it open in w.socket.
func (w *world) openWebSocket(ingress string) error {
	return w.openSocketTo(ingress, "api.shop.test", "/orders", "talon-shop-a")
}

// openSocketTo opens a WebSocket to path on host through the given ingress,
// as integration id, with any extra headers, and keeps it open in w.socket.
func (w *world) openSocketTo(ingress, host, path, id string, extra ...string) error {
	if err := w.start(); err != nil {
		return err
	}
	w.vendor(host)
	var (
		conn net.Conn
		err  error
	)
	head := []string{basic(id + ":x")}
	target := path
	switch ingress {
	case "forward_proxy":
		target = "https://" + host + path
		head = append(head, "Host: "+host)
	case "url_in_path":
		target = "/https://" + host + path
		head = append(head, "Host: localhost:8080")
	case "host_header":
		head = append(head, "Host: "+host)
	case "connect":
		if err := w.openTunnel(host, "443", id); err != nil {
			return err
		}
		conn, head = w.tunnel, []string{"Host: " + host}
	case "capture":
		if err := w.captureTunnel(host, "443", captureHeaders("4242", "/usr/bin/curl"), host, nil); err != nil {
			return err
		}
		conn, head = w.tunnel, []string{"Host: " + host}
	default:
		return failf("no ingress %s", ingress)
	}
	if conn == nil {
		if conn, err = w.dialProxy(); err != nil {
			return err
		}
		w.tunnels = append(w.tunnels, conn)
	}
	if conn == nil {
		return failf("no connection to the proxy: %v", w.tunnelErr)
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	head = append(append(head, extra...), "Connection: Upgrade", "Upgrade: websocket")
	if _, err := io.WriteString(conn, "GET "+target+" HTTP/1.1\r\n"+strings.Join(head, "\r\n")+"\r\n\r\n"); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return failf("the app got %d", resp.StatusCode)
	}
	w.socket = &openSocket{conn: conn, reader: reader}
	return nil
}

// callAndSession are the one call record and the one session record of the
// scenario, once the proxy has sent everything.
func (w *world) callAndSession() (receivedRecord, receivedRecord, error) {
	calls := w.records()
	sessions := w.receivers[0].sessionRecords()
	if len(calls) != 1 || len(sessions) != 1 {
		return receivedRecord{}, receivedRecord{}, failf("there are %d call records and %d session records, want one each", len(calls), len(sessions))
	}
	return calls[0], sessions[0], nil
}
