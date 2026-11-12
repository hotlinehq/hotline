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
	sc.Step(`^the extension opens a captured tunnel to (the address of )?(\S+):(\d+) for process (\d+) at "([^"]*)" and the app names (no site|\S+) in the handshake$`,
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
	sc.Step(`^the extension sends a CONNECT to api.talon.one:443 with (.+)$`, func(headers string) error {
		if err := w.start(); err != nil {
			return err
		}
		w.vendor("api.talon.one")
		conn, err := w.dialProxy()
		if err != nil {
			return err
		}
		w.tunnels = append(w.tunnels, conn)
		return w.sendConnect(conn, "api.talon.one:443", oddCaptureHeaders(headers))
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
	sc.Step(`^listen.connect lists (\S+) in passthrough$`, func(name string) error {
		w.listenConnect = fmt.Sprintf("  connect: { issuer: customer, passthrough: [%q] }\n", name)
		return nil
	})
	sc.Step(`^listen.connect lists a passthrough pattern that is not a name or a \*\. suffix$`, func() error {
		w.problem = `listen.connect: passthrough "api.*.com" must be a host name or "*." and a suffix`
		w.listenConnect = "  connect: { issuer: customer, passthrough: [\"api.*.com\"] }\n"
		return nil
	})

	sc.Step(`^the proxy listens on all interfaces$`, func() error {
		_, port, _ := net.SplitHostPort(freePort())
		w.addr = "0.0.0.0:" + port
		return nil
	})
	sc.Step(`^a program on another machine opens a tunnel to (\S+):(\d+) with the capture headers for process (\d+) at "([^"]*)"$`,
		func(host, port, pid, path string) error {
			if err := w.start(); err != nil {
				return err
			}
			w.vendor(host)
			outside, err := machineAddress()
			if err != nil {
				return err
			}
			_, proxyPort, _ := net.SplitHostPort(w.addr)
			conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(context.Background(), "tcp", net.JoinHostPort(outside, proxyPort))
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

	sc.Step(`^a program opens a connection to the proxy port and closes it without sending anything$`, func() error {
		if err := w.start(); err != nil {
			return err
		}
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(context.Background(), "tcp", w.addr)
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
	return []string{"Hotline-Capture: 1", "Hotline-Capture-Pid: " + pid, "Hotline-Capture-Program: " + path}
}

// oddCaptureHeaders are capture headers that are not usable, by what is
// wrong with them.
func oddCaptureHeaders(kind string) []string {
	switch kind {
	case "an unknown capture version":
		return []string{"Hotline-Capture: 2", "Hotline-Capture-Pid: 4242"}
	case "a process number that is not a number":
		return []string{"Hotline-Capture: 1", "Hotline-Capture-Pid: curl"}
	case "process number 0":
		return []string{"Hotline-Capture: 1", "Hotline-Capture-Pid: 0"}
	case "the capture header twice":
		return []string{"Hotline-Capture: 1", "Hotline-Capture: 1"}
	case "two process numbers":
		return []string{"Hotline-Capture: 1", "Hotline-Capture-Pid: 1", "Hotline-Capture-Pid: 2"}
	case "a program path over 4096 bytes":
		return []string{"Hotline-Capture: 1", "Hotline-Capture-Program: /" + strings.Repeat("a", 4096)}
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

// captureTunnel opens a tunnel as the extension does: a CONNECT to where the
// program called, with the capture headers, then the program's bytes.
// Without raw bytes the app sets up TLS, naming site in the handshake, as
// the app would. The result is kept like a tunnel's: w.tunnel, w.tunnelCerts
// and w.tunnelErr.
func (w *world) captureTunnel(host, port string, headers []string, site string, raw []byte) error {
	if err := w.start(); err != nil {
		return err
	}
	conn, err := w.dialProxy()
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

// machineAddress is an address of this machine that is not loopback, for a
// connection that comes from outside as far as the proxy can tell.
func machineAddress() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		if ip, ok := a.(*net.IPNet); ok && !ip.IP.IsLoopback() && ip.IP.To4() != nil {
			return ip.IP.String(), nil
		}
	}
	return "", failf("this machine has no address other than loopback")
}
