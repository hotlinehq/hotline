package proxy_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// captureSocketSteps are the steps about the capture listener, the socket file
// the capture extension sends its CONNECT to (Peter, 2026-11-12).
func captureSocketSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the capture listener is a socket file, whose issuer is customer too$`, func() error {
		w.captureFile = filepath.Join(w.dir, "capture.sock")
		return nil
	})
	sc.Step(`^a program sends GET http://api.talon.one/ on the capture socket$`, func() error {
		if err := w.start(); err != nil {
			return err
		}
		w.vendor("api.talon.one")
		conn, err := w.dialCapture()
		if err != nil {
			return err
		}
		w.tunnels = append(w.tunnels, conn)
		if _, err := io.WriteString(conn, "GET http://api.talon.one/ HTTP/1.1\r\nHost: api.talon.one\r\n\r\n"); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		w.resp = resp
		return nil
	})
	sc.Step(`^the capture socket file can be read and written by its owner only$`, func() error {
		info, err := os.Stat(w.captureFile)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
			return failf("the capture socket file is %v", info.Mode())
		}
		return nil
	})
	sc.Step(`^a socket file nobody listens on is left at the capture path$`, func() error {
		l, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", w.captureFile)
		if err != nil {
			return err
		}
		// Closing without removing leaves the file, as a crashed proxy does.
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		return l.Close()
	})
	sc.Step(`^another program listens on the capture path$`, func() error {
		l, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", w.captureFile)
		if err != nil {
			return err
		}
		w.listeners = append(w.listeners, l)
		w.busy = w.captureFile
		return nil
	})
	sc.Step(`^a plain file is at the capture path$`, func() error {
		return os.WriteFile(w.captureFile, []byte("notes"), 0o600)
	})
	sc.Step(`^a plain file is where the capture path's folder should be$`, func() error {
		folder := filepath.Join(w.dir, "folder")
		w.captureFile = filepath.Join(folder, "capture.sock")
		return os.WriteFile(folder, []byte("notes"), 0o600)
	})
	sc.Step(`^the plain file at the capture path is unchanged$`, func() error {
		data, err := os.ReadFile(w.captureFile)
		if err != nil || string(data) != "notes" {
			return failf("the file at the capture path is %q: %v", data, err)
		}
		return nil
	})
	sc.Step(`^there is no file at the capture path$`, func() error {
		if _, err := os.Stat(w.captureFile); !errors.Is(err, os.ErrNotExist) {
			return failf("the capture path holds a file: %v", err)
		}
		return nil
	})
	sc.Step(`^the capture listener (names no cert|names a cert that is not in certs|lists a passthrough pattern that is not a name|is a path of 120 bytes|is a relative path|is ~/hotline/capture.sock)$`, func(setting string) error {
		switch setting {
		case "names no cert":
			w.captureIssuer = " "
		case "names a cert that is not in certs":
			w.captureIssuer = "cert: gone, "
		case "lists a passthrough pattern that is not a name":
			w.capturePassthrough = []string{"api.*.com"}
		case "is a path of 120 bytes":
			w.capturePath = "/" + strings.Repeat("a", 119)
		case "is a relative path":
			w.capturePath = "capture.sock"
		default:
			if w.env == nil {
				w.env = map[string]string{}
			}
			w.env["HOME"] = w.dir
			w.capturePath = "~/hotline/capture.sock"
			w.captureFile = filepath.Join(w.dir, "hotline", "capture.sock")
		}
		return nil
	})
	sc.Step(`^the capture listener has http ports ([\d, and]+)$`, func(ports string) error {
		w.captureHTTP = "[" + strings.ReplaceAll(strings.ReplaceAll(ports, " and ", ", "), " ", "") + "]"
		return nil
	})
	sc.Step(`^the capture listener has no http ports$`, func() error {
		w.captureHTTP = "[]"
		return nil
	})
	sc.Step(`^the capture listener lists (\S+) in passthrough$`, func(name string) error {
		w.capturePassthrough = []string{name}
		return nil
	})
	sc.Step(`^the config has no proxy listener$`, func() error {
		w.noProxyPort, w.listenConnect = true, ""
		return nil
	})
	sc.Step(`^nothing listens on the proxy port$`, func() error {
		if conn, err := w.dialProxy(); err == nil {
			_ = conn.Close()
			return failf("something listens on the proxy port %s", w.addr)
		}
		return nil
	})
	sc.Step(`^the config file drops the capture listener$`, func() error {
		w.captureDropped = true
		w.writeFile("hotline-proxy.yaml", w.config())
		return nil
	})
	sc.Step(`^the capture socket is at hotline/capture.sock in the home folder$`, func() error {
		info, err := os.Stat(filepath.Join(w.dir, "hotline", "capture.sock"))
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSocket == 0 {
			return failf("the file there is %v, not a socket", info.Mode())
		}
		return nil
	})
}

// dialCapture opens a connection to the capture socket file.
func (w *world) dialCapture() (net.Conn, error) {
	return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(context.Background(), "unix", w.captureFile)
}
