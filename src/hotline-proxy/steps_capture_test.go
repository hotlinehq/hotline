package proxy_test

import (
	"bufio"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

func captureSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the extension (?:opens|has opened) a captured tunnel to (the address of )?(\S+):(\d+) for process (\d+) at "([^"]*)" and the app names (no site|\S+) in the handshake$`,
		func(address, host, port, pid, path, name string) error {
			dest := host
			if address != "" {
				dest = w.vendor(host).ip.String()
			}
			site := name
			switch {
			case name == "no site" && address == "":
				// An app that names no site but called by name pins the
				// vendor's certificate: it checks the issuer, not the name.
				site = ""
				w.vendor(host)
			case name == "no site":
				site = w.vendor(host).ip.String()
			}
			return w.captureTunnel(dest, port, captureHeaders(pid, path), site, nil)
		})
	sc.Step(`^the extension opens a captured tunnel to (\S+):(\d+) for process (\d+) at "([^"]*)" and sends plain HTTP bytes$`,
		func(host, port, pid, path string) error {
			w.vendor(host)
			return w.captureTunnel(host, port, captureHeaders(pid, path), "", []byte("GET / HTTP/1.1\r\nHost: "+host+"\r\n\r\n"))
		})
	sc.Step(`^the extension opens a captured tunnel to (\S+):(\d+) for process (\d+) at "([^"]*)" and the app sends a plain HTTP call inside it$`,
		func(host, port, pid, path string) error {
			w.vendor(host)
			conn, err := w.openCaptured(host, port, captureHeaders(pid, path))
			if err != nil {
				return err
			}
			w.tunnel = conn
			return w.callInTunnel("Host: " + host)
		})
	sc.Step(`^(\S+) on port (\d+) answers "([^"]*)" to what it gets$`, func(host, port, answer string) error {
		w.startPlainTalker(host, port, func(c net.Conn, got chan<- string) {
			b := make([]byte, 64)
			n, _ := c.Read(b)
			got <- string(b[:n])
			_, _ = io.WriteString(c, answer)
		})
		return nil
	})
	sc.Step(`^(\S+) on port (\d+) greets with "([^"]*)", then answers "([^"]*)" to what it gets$`, func(host, port, greeting, answer string) error {
		w.startPlainTalker(host, port, func(c net.Conn, got chan<- string) {
			_, _ = io.WriteString(c, greeting)
			b := make([]byte, 64)
			n, _ := c.Read(b)
			got <- string(b[:n])
			_, _ = io.WriteString(c, answer)
		})
		return nil
	})
	sc.Step(`^the extension opens a captured tunnel to (\S+):(\d+) for process (\d+) at "([^"]*)" and the app (?:waits for "([^"]*)", then )?sends "([^"]*)"$`,
		func(host, port, pid, path, greeting, message string) error {
			conn, err := w.openCaptured(host, port, captureHeaders(pid, path))
			if err != nil {
				return err
			}
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			if greeting != "" {
				got := make([]byte, len(greeting))
				if _, err := io.ReadFull(conn, got); err != nil || string(got) != greeting {
					return failf("the app got %q before sending, %v", got, err)
				}
			}
			if _, err := io.WriteString(conn, message); err != nil {
				return err
			}
			w.respBody, w.respErr = io.ReadAll(conn)
			_ = conn.Close()
			return nil
		})
	sc.Step(`^the extension opens a captured tunnel to (\S+):(\d+) for process (\d+) at "([^"]*)" and the app (closes it before sending anything|sends nothing yet)$`,
		func(host, port, pid, path, what string) error {
			conn, err := w.openCaptured(host, port, captureHeaders(pid, path))
			if err != nil || what == "sends nothing yet" {
				return err
			}
			return conn.Close()
		})
	sc.Step(`^the proxy stops within half a second$`, func() error {
		began := time.Now()
		w.settle()
		if took := time.Since(began); took > 500*time.Millisecond {
			return failf("the proxy took %v to stop", took)
		}
		return nil
	})
	sc.Step(`^the app gets "([^"]*)" back$`, func(want string) error {
		if string(w.respBody) != want {
			return failf("the app got %q (%v)", w.respBody, w.respErr)
		}
		return nil
	})
	sc.Step(`^the extension sends a CONNECT to (api.talon.one:443|api.talon.one:0) with (.+)$`, func(target, headers string) error {
		if err := w.start(); err != nil {
			return err
		}
		w.vendor("api.talon.one")
		conn, err := w.dialCapture()
		if err != nil {
			return err
		}
		w.tunnels = append(w.tunnels, conn)
		return w.sendConnect(conn, target, oddCaptureHeaders(headers))
	})
	sc.Step(`^the record says the program is process (\d+) at "([^"]*)"$`, func(pid, path string) error {
		if err := w.lastHas("process.pid", pid); err != nil {
			return err
		}
		return w.lastHas("process.executable.path", path)
	})
	sc.Step(`^the record names no program$`, func() error {
		if err := w.waitForRecords(1); err != nil {
			return err
		}
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		for _, key := range []string{"process.pid", "process.executable.path"} {
			if _, ok := record[key]; ok {
				return failf("the record has %s", key)
			}
		}
		return nil
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
	sc.Step(`^the app also trusts the vendors' own issuer$`, func() error {
		w.tunnelRoots.AddCert(w.issuer.cert)
		return nil
	})
	sc.Step(`^the connect part lists (\S+) in passthrough$`, func(name string) error {
		w.listenConnect = fmt.Sprintf("  connect: { cert: customer, passthrough: [%q] }\n", name)
		return nil
	})
	sc.Step(`^the connect part lists a passthrough pattern that is not a name or a \*\. suffix$`, func() error {
		w.problem = `listener proxy: connect: passthrough "api.*.com" must be a host name or "*." and a suffix`
		w.listenConnect = "  connect: { cert: customer, passthrough: [\"api.*.com\"] }\n"
		return nil
	})

	sc.Step(`^a program on this machine opens a tunnel on the proxy port to (\S+):(\d+) with the capture headers for process (\d+) at "([^"]*)"$`,
		func(host, port, pid, path string) error {
			if err := w.start(); err != nil {
				return err
			}
			w.vendor(host)
			conn, err := w.dialProxy()
			if err != nil {
				return err
			}
			w.tunnels = append(w.tunnels, conn)
			if err := w.sendConnect(conn, host+":"+port, captureHeaders(pid, path)); err != nil {
				return err
			}
			if w.resp.StatusCode != http.StatusOK {
				return failf("the proxy answered CONNECT with %d", w.resp.StatusCode)
			}
			w.secureTunnel(conn, host, port)
			return nil
		})

	sc.Step(`^a program opens the capture socket and closes it without sending anything$`, func() error {
		if err := w.start(); err != nil {
			return err
		}
		conn, err := w.dialCapture()
		if err != nil {
			return err
		}
		_ = conn.Close()
		// A record would be written at once; give it time to arrive.
		time.Sleep(300 * time.Millisecond)
		return nil
	})
}

// captureHeaders are the headers the extension adds to its CONNECT.
func captureHeaders(pid, path string) []string {
	return []string{"Hotline-Capture: 2", "Hotline-Capture-Pid: " + pid, "Hotline-Capture-Program: " + path}
}

// oddCaptureHeaders are capture headers that are not usable, by what is
// wrong with them.
func oddCaptureHeaders(kind string) []string {
	switch kind {
	case "good capture headers":
		return captureHeaders("4242", "/usr/bin/curl")
	case "an unknown capture version":
		return []string{"Hotline-Capture: 3", "Hotline-Capture-Pid: 4242"}
	case "capture version 1, from before the socket file":
		return []string{"Hotline-Capture: 1", "Hotline-Capture-Pid: 4242"}
	case "a process number that is not a number":
		return []string{"Hotline-Capture: 2", "Hotline-Capture-Pid: curl"}
	case "process number 0":
		return []string{"Hotline-Capture: 2", "Hotline-Capture-Pid: 0"}
	case "the capture header twice":
		return []string{"Hotline-Capture: 2", "Hotline-Capture: 2"}
	case "two process numbers":
		return []string{"Hotline-Capture: 2", "Hotline-Capture-Pid: 1", "Hotline-Capture-Pid: 2"}
	case "a program path over 4096 bytes":
		return []string{"Hotline-Capture: 2", "Hotline-Capture-Program: /" + strings.Repeat("a", 4096)}
	}
	return nil
}

// sendConnect sends a CONNECT for authority with extra header lines and
// keeps the answer in w.resp.
func (w *world) sendConnect(conn net.Conn, authority string, headers []string) error {
	var head strings.Builder
	fmt.Fprintf(&head, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", authority, authority)
	for _, h := range headers {
		head.WriteString(h + "\r\n")
	}
	head.WriteString("\r\n")
	if _, err := io.WriteString(conn, head.String()); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	_ = conn.SetReadDeadline(time.Time{})
	w.resp = resp
	return nil
}

// openCaptured sends the extension's CONNECT for host:port on the capture
// socket and gives the open tunnel, before the app sends anything.
func (w *world) openCaptured(host, port string, headers []string) (net.Conn, error) {
	if err := w.start(); err != nil {
		return nil, err
	}
	conn, err := w.dialCapture()
	if err != nil {
		return nil, err
	}
	w.tunnels = append(w.tunnels, conn)
	if err := w.sendConnect(conn, net.JoinHostPort(host, port), headers); err != nil {
		return nil, err
	}
	if w.resp.StatusCode != http.StatusOK {
		return nil, failf("the proxy answered CONNECT with %d", w.resp.StatusCode)
	}
	return conn, nil
}

// captureTunnel opens a tunnel as the extension does: a CONNECT to where the
// program called, with the capture headers, then the program's bytes.
// Without raw bytes the app sets up TLS, naming site in the handshake, as
// the app would. The result is kept like a tunnel's: w.tunnel, w.tunnelCerts
// and w.tunnelErr.
func (w *world) captureTunnel(host, port string, headers []string, site string, raw []byte) error {
	if err := w.start(); err != nil {
		return err
	}
	conn, err := w.dialCapture()
	if err != nil {
		return err
	}
	w.tunnels = append(w.tunnels, conn)
	if err := w.sendConnect(conn, net.JoinHostPort(host, port), headers); err != nil {
		return err
	}
	if w.resp.StatusCode != http.StatusOK {
		return failf("the proxy answered CONNECT with %d", w.resp.StatusCode)
	}
	if raw != nil {
		if _, err := conn.Write(raw); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.Copy(io.Discard, conn)
		w.tunnelErr = errProxyClosed
		return w.waitForRecords(1)
	}
	w.interceptHost = cmp.Or(site, host)
	if net.ParseIP(site) == nil && site != "" {
		w.vendor(site)
	}
	roots := w.tunnelRoots.Clone()
	// A relayed connection shows the vendor's own certificate.
	roots.AddCert(w.issuer.cert)
	config := &tls.Config{RootCAs: roots, ServerName: site, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12}
	if site == "" {
		// No site name is sent, and the certificate is checked by its
		// issuer alone, as an app that pins it does.
		config.InsecureSkipVerify = true
		config.VerifyConnection = func(cs tls.ConnectionState) error {
			_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots})
			return err
		}
	}
	client := tls.Client(conn, config)
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	w.tunnelErr = client.HandshakeContext(context.Background())
	_ = client.SetDeadline(time.Time{})
	if state := client.ConnectionState(); state.HandshakeComplete {
		w.tunnelCerts = append(w.tunnelCerts, state.PeerCertificates[0])
		w.tunnel = client
	}
	return nil
}
