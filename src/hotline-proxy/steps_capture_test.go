package proxy_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// captureControl is the extension's control connection, and the app lists
// the proxy sent on it.
type captureControl struct {
	conn   net.Conn
	reader *bufio.Reader
}

// words splits "a, b and c" into its names.
func words(list string) []string {
	return strings.FieldsFunc(strings.ReplaceAll(list, " and ", ","), func(r rune) bool { return r == ',' || r == ' ' })
}

func captureSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the proxy captures the apps (.+)$`, func(apps string) error {
		w.captureApps = words(apps)
		return nil
	})
	sc.Step(`^the proxy captures no apps$`, func() error {
		w.captureApps = nil
		return nil
	})
	sc.Step(`^the extension opens a flow to (the address of )?(\S+):(\d+) for process (\d+) at "([^"]*)" and the app names (no site|\S+) in the handshake$`,
		func(address, host, port, pid, path, name string) error {
			dest := host
			if address != "" {
				dest = w.vendor(host).ip.String()
			}
			site := name
			if name == "no site" {
				site = w.vendor(host).ip.String()
			}
			return w.captureFlow(dest, port, pid, path, site, nil)
		})
	sc.Step(`^the extension opens a flow to (\S+):(\d+) for process (\d+) at "([^"]*)" and sends plain HTTP bytes$`,
		func(host, port, pid, path string) error {
			w.vendor(host)
			return w.captureFlow(host, port, pid, path, "", []byte("GET / HTTP/1.1\r\nHost: "+host+"\r\n\r\n"))
		})
	sc.Step(`^the record says the program is process (\d+) at "([^"]*)"$`, func(pid, path string) error {
		if err := w.lastHas("process.pid", pid); err != nil {
			return err
		}
		return w.lastHas("process.executable.path", path)
	})
	sc.Step(`^the test vendor got the call over https on port (\d{4,5})$`, func(port string) error {
		c, err := w.onlyCall("api.talon.one")
		if err != nil {
			return err
		}
		if c.scheme != "https" || c.port != port {
			return failf("the vendor got the call over %s on port %s", c.scheme, c.port)
		}
		return nil
	})

	sc.Step(`^the extension (?:opens|has opened) the control connection$`, w.openControl)
	sc.Step(`^the extension receives the app list (.+) and a skip rule for Hotline's own process number$`, func(apps string) error {
		want := append(words(apps), "!"+strconv.Itoa(os.Getpid()))
		return w.controlReceives(want)
	})
	sc.Step(`^the extension sends (.+) as its first message$`, func(kind string) error {
		if err := w.start(); err != nil {
			return err
		}
		conn, err := w.dialCapture()
		if err != nil {
			return err
		}
		if _, err := conn.Write(oddCaptureHello(kind)); err != nil {
			return err
		}
		if unix, ok := conn.(*net.TCPConn); ok {
			_ = unix.CloseWrite()
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.Copy(io.Discard, conn)
		return nil
	})

	sc.Step(`^the extension's control connection is closed$`, func() error {
		if w.control == nil {
			return failf("the extension has no control connection")
		}
		_ = w.control.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if n, err := w.control.reader.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
			return failf("the control connection gave %d bytes and %v, want it closed", n, err)
		}
		return nil
	})

	sc.Step(`^the proxy is asked to capture but the config has no listen.intercept$`, func() error {
		w.problem = "capturing apps needs listen.intercept"
		w.captureApps = []string{"curl"}
		w.intercept = nil
		return nil
	})
	sc.Step(`^the proxy is asked to capture while listen.intercept listens on all interfaces$`, func() error {
		w.problem = "capturing apps needs listen.intercept on a loopback address"
		w.captureApps = []string{"curl"}
		w.useIssuer(newIssuer())
		_, port, _ := net.SplitHostPort(freePort())
		w.intercept = &interceptConf{address: "0.0.0.0:" + port, issuer: "customer"}
		return nil
	})
	sc.Step(`^the proxy is asked to capture an app that is just a skip mark$`, func() error {
		w.problem = "an app must be a program name or a process number"
		w.captureApps = []string{"!"}
		w.useIssuer(newIssuer())
		w.intercept = &interceptConf{address: freePort(), issuer: "customer"}
		return nil
	})
	sc.Step(`^the proxy is asked to capture an app with a space in it$`, func() error {
		w.problem = "an app must be a program name or a process number"
		w.captureApps = []string{"my app"}
		w.useIssuer(newIssuer())
		w.intercept = &interceptConf{address: freePort(), issuer: "customer"}
		return nil
	})
}

// dialCapture connects to the intercept port, as the extension does, and
// keeps the connection to close with the world.
func (w *world) dialCapture() (net.Conn, error) {
	if w.interceptAddr == "" {
		return nil, failf("the scenario has no listen.intercept")
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(context.Background(), "tcp", w.interceptAddr)
	if err != nil {
		return nil, err
	}
	w.tunnels = append(w.tunnels, conn)
	return conn, nil
}

// frame is a message the way the extension sends it: 4 bytes with the size
// of the body, then the body as JSON.
func frame(body []byte) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...) //nolint:gosec // short test messages
}

// captureFlow opens a flow as the extension does: the hello naming where the
// program called, then the program's bytes. Without raw bytes the app sets
// up TLS, naming site in the handshake, as the app would. The result is kept
// like a tunnel's: w.tunnel, w.tunnelCerts and w.tunnelErr.
func (w *world) captureFlow(host, port, pid, path, site string, raw []byte) error {
	if err := w.start(); err != nil {
		return err
	}
	conn, err := w.dialCapture()
	if err != nil {
		return err
	}
	portNumber, _ := strconv.Atoi(port)
	processNumber, _ := strconv.Atoi(pid)
	hello, err := json.Marshal(struct {
		Version     int    `json:"version"`
		Role        string `json:"role"`
		Transport   string `json:"transport"`
		Host        string `json:"host"`
		Port        int    `json:"port"`
		PID         int    `json:"pid"`
		ProcessName string `json:"processName"`
	}{1, "flow", "tcp", host, portNumber, processNumber, path})
	if err != nil {
		return err
	}
	if raw != nil {
		_, err := conn.Write(append(frame(hello), raw...))
		if err != nil {
			return err
		}
		if unix, ok := conn.(*net.TCPConn); ok {
			_ = unix.CloseWrite()
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.Copy(io.Discard, conn)
		w.tunnelErr = errProxyClosed
		return w.waitForRecords(1)
	}
	if _, err := conn.Write(frame(hello)); err != nil {
		return err
	}
	w.interceptHost = site
	if net.ParseIP(site) == nil {
		w.vendor(site)
	}
	roots := w.tunnelRoots.Clone()
	// A relayed connection shows the vendor's own certificate.
	roots.AddCert(w.issuer.cert)
	client := tls.Client(conn, &tls.Config{RootCAs: roots, ServerName: site, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	w.tunnelErr = client.HandshakeContext(context.Background())
	_ = client.SetDeadline(time.Time{})
	if state := client.ConnectionState(); state.HandshakeComplete {
		w.tunnelCerts = append(w.tunnelCerts, state.PeerCertificates[0])
		w.tunnel = client
	}
	return nil
}

// openControl connects as the extension's control connection.
func (w *world) openControl() error {
	if err := w.start(); err != nil {
		return err
	}
	conn, err := w.dialCapture()
	if err != nil {
		return err
	}
	if _, err := conn.Write(frame([]byte(`{"version":1,"role":"control"}`))); err != nil {
		return err
	}
	w.control = &captureControl{conn: conn, reader: bufio.NewReader(conn)}
	return nil
}

// controlReceives reads the next app list from the control connection and
// compares it with want.
func (w *world) controlReceives(want []string) error {
	if w.control == nil {
		return failf("the extension has no control connection")
	}
	_ = w.control.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var size [4]byte
	if _, err := io.ReadFull(w.control.reader, size[:]); err != nil {
		return failf("no app list arrived: %v", err)
	}
	body := make([]byte, binary.BigEndian.Uint32(size[:]))
	if _, err := io.ReadFull(w.control.reader, body); err != nil {
		return failf("the app list was cut short: %v", err)
	}
	var update struct {
		Version int      `json:"version"`
		Entries []string `json:"entries"`
	}
	if err := json.Unmarshal(body, &update); err != nil {
		return failf("the app list is not JSON: %v", err)
	}
	if update.Version != 1 || !slices.Equal(update.Entries, want) {
		return failf("the extension got version %d with %v, want version 1 with %v", update.Version, update.Entries, want)
	}
	return nil
}

// oddCaptureHello is a first message that is not a usable hello, by what is
// wrong with it.
func oddCaptureHello(kind string) []byte {
	body := func(s string) []byte { return frame([]byte(s)) }
	switch kind {
	case "a size over 16 KiB":
		return binary.BigEndian.AppendUint32(nil, 16*1024+1)
	case "a body that is not JSON":
		return body("hello")
	case "a message cut short":
		return append(binary.BigEndian.AppendUint32(nil, 50), "{}"...)
	case "a hello with an unknown version":
		return body(`{"version":2,"role":"flow","transport":"tcp","host":"api.talon.one","port":443}`)
	case "a flow with no host":
		return body(`{"version":1,"role":"flow","transport":"tcp","port":443}`)
	case "a flow with port 0":
		return body(`{"version":1,"role":"flow","transport":"tcp","host":"api.talon.one","port":0}`)
	case "a flow that is not TCP":
		return body(`{"version":1,"role":"flow","transport":"udp","host":"api.talon.one","port":443}`)
	}
	return nil
}
