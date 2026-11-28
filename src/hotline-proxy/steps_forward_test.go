package proxy_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cucumber/godog"
)

func forwardSteps(sc *godog.ScenarioContext, sut *forwardSUT) {
	sc.Step(`^an api with TLS origination on(?:, the default)?$`, func() error {
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.talonAPI("")))
		return nil
	})
	sc.Step(`^an api with tlsOrigination set to false$`, func() error {
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.talonAPI("    tlsOrigination: false\n")))
		return nil
	})
	sc.Step(`^a not-passed call with the built-in settings$`, func() error {
		sut.app.appID = ""
		return nil
	})
	sc.Step(`^a not-passed entry with tlsOrigination set to false$`, func() error {
		sut.app.appID = ""
		sut.apis.addIntegration("  - id: not-passed\n    tlsOrigination: false\n")
		return nil
	})
	sc.Step(`^the proxy calls the vendor over (https?) on port (\d+)$`, sut.app.calledOver)

	sc.Step(`^an app call with Proxy-Authorization, Authorization, Connection and a JSON body$`, func() error { return nil })
	sc.Step(`^the proxy forwards it$`, func() error {
		return sut.forwardFull("https://api.talon.one/v2/customer_sessions/sess-1?dry=true&b=%2F")
	})
	sc.Step(`^the proxy forwards it to "([^"]+)"$`, sut.forwardFull)
	sc.Step(`^the method, path, query and body reach the vendor unchanged$`, func() error {
		c, err := sut.vendors.onlyCall("api.talon.one")
		if err != nil {
			return err
		}
		if c.method != "PUT" || c.uri != "/v2/customer_sessions/sess-1?dry=true&b=%2F" || string(c.body) != `{"customerSession":{"state":"open"}}` {
			return failf("the vendor got %s %s with %q", c.method, c.uri, c.body)
		}
		return nil
	})
	sc.Step(`^an app call$`, func() error { return nil })
	sc.Step(`^the vendor gets no X-Forwarded-For or Via header$`, func() error {
		return sut.vendorHeaders(nil, []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded", "Via"})
	})
	sc.Step(`^the proxy adds no header of its own, such as Accept-Encoding or User-Agent$`, func() error {
		return sut.vendorHeaders(nil, []string{"Accept-Encoding", "User-Agent"})
	})
	sc.Step(`^a vendor with a vendorTimeout of 10 seconds whose response goes quiet after its first part$`, func() error {
		sut.proxy.clock = &fakeClock{}
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.talonAPI("    vendorTimeout: 10s\n")))
		sut.vendors.quiet, sut.vendors.cutOff = make(chan struct{}), make(chan struct{})
		quiet, cutOff := sut.vendors.quiet, sut.vendors.cutOff
		sut.vendors.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(rw, "first part, ")
			rw.(http.Flusher).Flush()
			select {
			case <-quiet:
				_, _ = io.WriteString(rw, "last part")
			case <-r.Context().Done():
				close(cutOff)
			}
		})
		return nil
	})
	sc.Step(`^the response stays quiet for (\d+) seconds$`, func(seconds int) error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		// The proxy starts waits of its own, such as the ledger's batch
		// timer; the call's waits are the ones after these.
		ownWaits := sut.proxy.clock.armed()
		sut.vendors.vendor("api.talon.one")
		conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", sut.proxy.addr)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, err = io.WriteString(conn, "GET https://api.talon.one/v2/x HTTP/1.1\r\nHost: api.talon.one\r\n"+basic("talon-shop-a:x")+"\r\n\r\n")
		if err != nil {
			return err
		}
		reader := bufio.NewReader(conn)
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		sut.app.resp = resp
		first := make([]byte, len("first part, "))
		if _, err := io.ReadFull(sut.app.resp.Body, first); err != nil {
			return err
		}
		// Wait until the proxy waits for the next part, then let time pass.
		for deadline := time.Now().Add(waitLimit); sut.proxy.clock.waitingSince(ownWaits) == 0; {
			if time.Now().After(deadline) {
				return failf("the proxy never waited for the response's next part")
			}
			pause(time.Millisecond)
		}
		sut.proxy.clock.pass(time.Duration(seconds) * time.Second)
		// Give a proxy that gave up the time to tell the vendor, before the
		// vendor may send the rest.
		select {
		case <-sut.vendors.cutOff:
		case <-time.After(100 * time.Millisecond):
		}
		close(sut.vendors.quiet)
		rest, err := io.ReadAll(sut.app.resp.Body)
		first = append(first, rest...)
		sut.app.respBody, sut.app.respErr = first, err
		return nil
	})
	sc.Step(`^the app gets the whole response and the record has no error.type$`, func() error {
		if sut.app.respErr != nil || string(sut.app.respBody) != "first part, last part" {
			return failf("the app got %q and %v", sut.app.respBody, sut.app.respErr)
		}
		record, err := sut.otlp.record()
		if err != nil {
			return err
		}
		return hasNo(record, "error.type")
	})
	sc.Step(`^the response to the app breaks off and the record says http_response_incomplete$`, func() error {
		if sut.app.respErr == nil {
			return failf("the app got the whole response %q", sut.app.respBody)
		}
		record, err := sut.otlp.record()
		if err != nil {
			return err
		}
		return has(record, "error.type", "http_response_incomplete")
	})
	sc.Step(`^a vendor that answers 413 without reading the body$`, func() error {
		sut.vendors.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			rw.WriteHeader(http.StatusRequestEntityTooLarge)
		})
		return nil
	})
	sc.Step(`^an app sends a body slowly, in parts$`, func() error {
		body, send := io.Pipe()
		go func() {
			for range 10 {
				if _, err := send.Write(bytes.Repeat([]byte("p"), 1024)); err != nil {
					return
				}
				pause(20 * time.Millisecond)
			}
			_ = send.Close()
		}()
		defer func() { _ = body.Close() }()
		return sut.app.send(request{
			method:  "PUT",
			target:  sut.app.target,
			headers: []string{basic("talon-shop-a:x")},
			body:    body,
			length:  -1,
			chunked: true,
		})
	})
	sc.Step(`^the app gets (\d+)$`, func(status int) error {
		if sut.app.resp == nil || sut.app.resp.StatusCode != status {
			return failf("the app got %v (%v)", sut.app.resp, sut.app.respErr)
		}
		return nil
	})
	sc.Step(`^a vendor that accepts a WebSocket upgrade and sends back what it gets$`, func() error {
		sut.vendors.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Upgrade") != "websocket" {
				rw.WriteHeader(http.StatusBadRequest)
				return
			}
			rw.Header().Set("Connection", "Upgrade")
			rw.Header().Set("Upgrade", "websocket")
			rw.WriteHeader(http.StatusSwitchingProtocols)
			conn, buf, err := http.NewResponseController(rw).Hijack()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			got := make([]byte, 4)
			if _, err := io.ReadFull(buf, got); err == nil {
				_, _ = conn.Write(got)
			}
		})
		return nil
	})
	sc.Step(`^an app opens a WebSocket through the proxy and sends "(\w+)"$`, func(message string) error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", sut.proxy.addr)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, err = io.WriteString(conn, "GET https://api.talon.one/v2/live HTTP/1.1\r\nHost: api.talon.one\r\n"+
			basic("talon-shop-a:x")+"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		if err != nil {
			return err
		}
		reader := bufio.NewReader(conn)
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		sut.app.resp = resp
		if _, err := io.WriteString(conn, message); err != nil {
			return err
		}
		sut.app.respBody = make([]byte, len(message))
		_, err = io.ReadFull(reader, sut.app.respBody)
		return err
	})
	sc.Step(`^a shop spec where GET /orders answers 101 Switching Protocols$`, func() error {
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.specAPI("api.shop.test", websocketSpec, "")))
		return nil
	})
	sc.Step(`^a shop spec where GET /orders answers 101 Switching Protocols, with content full and the key header (\S+) in its security section$`, func(header string) error {
		spec := websocketSpec + "components:\n  securitySchemes:\n    key: { type: apiKey, in: header, name: " + header + " }\n"
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.specAPI("api.shop.test", spec, "")) + "    content: full\n")
		return nil
	})
	sc.Step(`^an app opens a WebSocket to the shop through the proxy with "([^"]+)", reads "(\w+)" and closes$`, func(header, message string) error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		return sut.websocketOnce("https://api.shop.test/orders", message, header)
	})
	sc.Step(`^the proxy's HTTP server logged nothing$`, func() error {
		sut.proxy.settle()
		for _, line := range strings.Split(sut.proxy.logs.String(), "\n") {
			if strings.Contains(line, `msg="http server"`) {
				return failf("the proxy's HTTP server logged %s", line)
			}
		}
		return nil
	})
	sc.Step(`^the shop vendor accepts a WebSocket upgrade, sends "(\w+)" and closes at once$`, func(message string) error {
		sut.vendors.vendor("api.shop.test").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Connection", "Upgrade")
			rw.Header().Set("Upgrade", "websocket")
			rw.WriteHeader(http.StatusSwitchingProtocols)
			conn, _, err := http.NewResponseController(rw).Hijack()
			if err != nil {
				return
			}
			_, _ = io.WriteString(conn, message)
			_ = conn.Close()
		})
		return nil
	})
	sc.Step(`^an app opens a WebSocket to the shop through the proxy, reads "(\w+)" and closes, (\d+) times$`, func(message string, n int) error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		for range n {
			if err := sut.websocketOnce("https://api.shop.test/orders", message); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^there are (\d+) records, each with http.response.status_code (\d+)$`, func(n, status int) error {
		records := sut.otlp.records()
		if len(records) != n {
			return failf("there are %d records, want %d", len(records), n)
		}
		for _, r := range records {
			if err := has(r.attrs, "http.response.status_code", status); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the app gets 101 Switching Protocols and "(\w+)" back$`, func(message string) error {
		if sut.app.resp.StatusCode != http.StatusSwitchingProtocols || string(sut.app.respBody) != message {
			return failf("the app got %d and %q", sut.app.resp.StatusCode, sut.app.respBody)
		}
		return nil
	})
	sc.Step(`^the record says http.response.status_code (\d+)$`, func(status int) error {
		record, err := sut.otlp.record()
		if err != nil {
			return err
		}
		return has(record, "http.response.status_code", status)
	})
	sc.Step(`^Proxy-Authorization is removed$`, func() error { return sut.vendorHeaders(nil, []string{"Proxy-Authorization"}) })
	sc.Step(`^Authorization and every other end-to-end header are kept$`, func() error {
		return sut.vendorHeaders([]string{"Authorization", "Content-Type", "X-Trace"}, nil)
	})
	sc.Step(`^headers for one hop only, such as Connection and Proxy-Connection, are removed as HTTP requires$`, func() error {
		return sut.vendorHeaders(nil, []string{"Connection", "X-Hop", "Proxy-Connection"})
	})
	sc.Step(`^Host is set to the vendor host from the (?:request line|path)$`, func() error {
		c, err := sut.vendors.onlyCall("api.talon.one")
		if err != nil {
			return err
		}
		if c.host != "api.talon.one" {
			return failf("the vendor got Host %s", c.host)
		}
		return nil
	})

	sc.Step(`^a vendor that answers slowly with a large response$`, func() error { return nil })
	sc.Step(`^an app sends a large request body$`, sut.streamBothWays)
	sc.Step(`^the body reaches the vendor before the app has finished sending it$`, func() error {
		if !sut.stream.bodyEarly.Load() {
			return failf("the vendor got nothing before the app finished sending")
		}
		return nil
	})
	sc.Step(`^the response reaches the app before the vendor has finished sending it$`, func() error {
		if !sut.stream.responseEarly.Load() {
			return failf("the app got nothing before the vendor finished sending")
		}
		return nil
	})

	sc.Step(`^a vendor that fails the first call$`, func() error {
		sut.vendors.vendor("api.talon.one").setAnswer(func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		})
		return nil
	})
	sc.Step(`^an app sends one call$`, func() error { return sut.app.defaultCall() })
	sc.Step(`^the vendor sees exactly one call$`, func() error {
		_, err := sut.vendors.onlyCall("api.talon.one")
		return err
	})

	sc.Step(`^a vendor that answers (\d+) with headers and a body$`, func(status int) error {
		sut.vendors.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("X-Vendor", "v1")
			rw.Header().Set("Content-Type", "text/plain")
			rw.WriteHeader(status)
			_, _ = io.WriteString(rw, "vendor says hello")
		})
		return nil
	})
	sc.Step(`^an app calls (?:it )?through the proxy$`, func() error { return sut.app.defaultCall() })
	sc.Step(`^the app gets (\d+) with the same headers and body$`, func(status int) error {
		if sut.app.resp == nil || sut.app.resp.StatusCode != status || sut.app.resp.Header.Get("X-Vendor") != "v1" ||
			sut.app.resp.Header.Get("Content-Type") != "text/plain" || string(sut.app.respBody) != "vendor says hello" {
			return failf("the app got %+v with %q", sut.app.resp, sut.app.respBody)
		}
		return nil
	})
	sc.Step(`^there is no Proxy-Status header$`, func() error {
		if v := sut.app.resp.Header.Get("Proxy-Status"); v != "" {
			return failf("the app got Proxy-Status %s", v)
		}
		return nil
	})

	sc.Step(`^an app using HTTP/1.1 and a vendor that offers (HTTP/2 and HTTP/1.1|HTTP/1.1 only)$`, func(offers string) error {
		if offers == "HTTP/1.1 only" {
			v := sut.vendors.vendor("api.talon.one")
			s := httptest.NewUnstartedServer(v.record("https", "443"))
			s.TLS = sut.vendorTLS("api.talon.one")
			s.TLS.NextProtos = []string{"http/1.1"}
			s.StartTLS()
			sut.vendors.routeTo("api.talon.one", "443", s.Listener)
			v.servers = append(v.servers, s)
		}
		return nil
	})
	sc.Step(`^the call goes through$`, func() error {
		if sut.app.resp == nil {
			if err := sut.app.defaultCall(); err != nil {
				return err
			}
		}
		if sut.app.resp == nil || sut.app.resp.StatusCode != http.StatusOK {
			return failf("the app got %v with %q", sut.app.resp, sut.app.respBody)
		}
		return nil
	})
	sc.Step(`^the proxy talks (\S+) to the vendor and (\S+) to the app$`, func(toVendor, toApp string) error {
		c, err := sut.vendors.onlyCall("api.talon.one")
		if err != nil {
			return err
		}
		want := map[string]string{"HTTP/2": "HTTP/2.0", "HTTP/1.1": "HTTP/1.1"}
		if c.proto != want[toVendor] || sut.app.resp.Proto != want[toApp] {
			return failf("the vendor got %s and the app %s", c.proto, sut.app.resp.Proto)
		}
		return nil
	})

	sc.Step(`^a vendor whose certificate authority is in the system trust store$`, func() error { return nil })
	sc.Step(`^a vendor whose certificate is (self-signed|expired|for another host name)$`, func(problem string) error {
		switch problem {
		case "self-signed":
			sut.vendors.replaceTLS("api.talon.one", sut.vendors.authority.leaf("api.talon.one", time.Now().Add(time.Hour), true))
		case "expired":
			sut.vendors.replaceTLS("api.talon.one", sut.vendors.authority.leaf("api.talon.one", time.Now().Add(-time.Hour), false))
		default:
			sut.vendors.replaceTLS("api.talon.one", sut.vendors.authority.leaf("other.example", time.Now().Add(time.Hour), false))
		}
		return nil
	})
	sc.Step(`^the app gets (\d+) with "Proxy-Status: ([^"]+)"$`, func(status int, proxyStatus string) error {
		if sut.app.resp == nil || sut.app.resp.StatusCode != status || sut.app.resp.Header.Get("Proxy-Status") != proxyStatus {
			return failf("the app got %v, want %d with Proxy-Status %s", sut.app.resp, status, proxyStatus)
		}
		return nil
	})
	sc.Step(`^the config file format$`, func() error {
		sut.vendors.replaceTLS("api.talon.one", sut.vendors.authority.leaf("api.talon.one", time.Now().Add(time.Hour), true))
		return nil
	})
	sc.Step(`^anyone looks for a setting to skip the certificate check$`, func() error {
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.talonAPI(
			"    insecureSkipVerify: true\n    tlsSkipVerify: true\n    skipCertificateCheck: true\n    verifyCertificate: false\n")))
		return sut.app.defaultCall()
	})
	sc.Step(`^there is none$`, func() error {
		if sut.app.resp == nil || sut.app.resp.StatusCode != http.StatusBadGateway {
			return failf("the app got %v, want 502", sut.app.resp)
		}
		return nil
	})

	sc.Step(`^(a vendor host that does not exist|a DNS lookup that takes too long|a vendor that refuses the connection|a vendor that takes too long to connect|a TLS handshake that fails for a reason other than the certificate|a TLS handshake that takes too long|a vendor slower than vendorTimeout|a vendor whose network cannot be reached|a vendor that closes the connection before answering)$`, sut.situation)
	sc.Step(`^the record's error.type is (\S+)$`, sut.errorType)
	sc.Step(`^error.type is (\S+)$`, sut.errorType)

	sc.Step(`^a vendor that stops after sending half of its response$`, func() error {
		sut.vendors.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Length", "100")
			_, _ = io.WriteString(rw, strings.Repeat("a", 50))
			rw.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		})
		return nil
	})
	sc.Step(`^the response to the app is cut$`, func() error {
		if sut.app.respErr == nil {
			return failf("the app got a whole response: %q", sut.app.respBody)
		}
		return nil
	})

	sc.Step(`^a call the proxy refuses$`, func() error { return nil })
	sc.Step(`^the app sends it$`, func() error { return sut.app.call("PUT", "ftp://api.talon.one/v2/customer_sessions/1") })
	sc.Step(`^the vendor gets no part of it$`, sut.vendors.nothingSent)

	sc.Step(`^a vendor that answers after (\d+) milliseconds and a vendorTimeout of (\d+) seconds$`, func(after, timeout int) error {
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.talonAPI(sprintf("    vendorTimeout: %ds\n", timeout))))
		sut.vendors.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			pause(time.Duration(after) * time.Millisecond)
			rw.WriteHeader(http.StatusOK)
		})
		return nil
	})
	sc.Step(`^the app disconnects after (\d+) milliseconds?$`, func(after int) error {
		return sut.app.send(request{
			method:      "PUT",
			target:      sut.app.target,
			headers:     []string{basic("talon-shop-a:x")},
			length:      -1,
			hangUpAfter: time.Duration(after) * time.Millisecond,
		})
	})
	sc.Step(`^the proxy keeps waiting for the vendor$`, func() error {
		sut.proxy.settle()
		_, err := sut.vendors.onlyCall("api.talon.one")
		return err
	})
	sc.Step(`^the record holds the vendor's real status and timings$`, func() error {
		record, err := sut.otlp.record()
		if err != nil {
			return err
		}
		if total, _ := secondsOf(record["hotline.timing.total_us"]); total < 0.3 {
			return failf("total is %v, want at least the vendor's 0.3 seconds", record["hotline.timing.total_us"])
		}
		return has(record, "http.response.status_code", 200)
	})
	sc.Step(`^the record says hotline.client.cancelled (true|false)$`, func(cancelled string) error {
		record, err := sut.otlp.record()
		if err != nil {
			return err
		}
		return has(record, "hotline.client.cancelled", cancelled)
	})
	sc.Step(`^a vendor that answers normally$`, func() error { return nil })
	sc.Step(`^the app waits for the answer$`, func() error { return sut.app.defaultCall() })
}

func (sut *appSUT) calledOver(scheme, port string) error {
	var calls []vendorCall
	for _, v := range sut.vendors.vendors {
		calls = append(calls, v.received()...)
	}
	if len(calls) != 1 || calls[0].scheme != scheme || calls[0].port != port {
		return failf("the vendors got %+v, want one call over %s on port %s; the app got %v", calls, scheme, port, sut.resp)
	}
	return nil
}

func (sut *vendorsSUT) onlyCall(host string) (vendorCall, error) {
	calls := sut.vendor(host).received()
	if len(calls) != 1 {
		return vendorCall{}, failf("%s got %d calls, want one", host, len(calls))
	}
	return calls[0], nil
}

func (sut *forwardSUT) vendorHeaders(present, absent []string) error {
	c, err := sut.vendors.onlyCall("api.talon.one")
	if err != nil {
		return err
	}
	for _, h := range present {
		if c.header.Get(h) == "" {
			return failf("the vendor got no %s; it got %v", h, c.header)
		}
	}
	for _, h := range absent {
		if v, found := c.header[http.CanonicalHeaderKey(h)]; found {
			return failf("the vendor got %s: %v", h, v)
		}
	}
	return nil
}

func (sut *forwardSUT) errorType(name string) error {
	if sut.app.resp == nil && sut.app.respErr == nil {
		if err := sut.app.defaultCall(); err != nil {
			return err
		}
	}
	record, err := sut.otlp.record()
	if err != nil {
		return err
	}
	return has(record, "error.type", name)
}

// situation sets up one way a vendor call can fail.
func (sut *forwardSUT) situation(situation string) error {
	sut.proxy.clock = &fakeClock{}
	sut.apis.addIntegration(integration("talon-shop-a", sut.apis.talonAPI("    vendorTimeout: 10s\n")))
	v := sut.vendors.vendor("api.talon.one")
	address := net.JoinHostPort(v.ip.String(), "443")
	switch situation {
	case "a vendor host that does not exist":
		sut.vendors.missing["no-such-host.talon.one"] = true
		sut.app.target = "https://no-such-host.talon.one/v2/customer_sessions/1"
	case "a DNS lookup that takes too long":
		sut.vendors.network.setHang("api.talon.one")
	case "a vendor that refuses the connection":
		sut.vendors.refuse("api.talon.one", "443")
	case "a vendor that takes too long to connect":
		sut.vendors.network.setHang(address)
	case "a vendor whose network cannot be reached":
		sut.vendors.network.setUnreachable(address)
	case "a TLS handshake that fails for a reason other than the certificate":
		sut.vendors.listen(func(c net.Conn) { _, _ = io.WriteString(c, "HTTP/1.1 400 Bad Request\r\n\r\n") })
	case "a TLS handshake that takes too long":
		sut.vendors.listen(func(net.Conn) { sut.vendors.stuck() })
	case "a vendor slower than vendorTimeout":
		v.setAnswer(func(_ http.ResponseWriter, r *http.Request) {
			sut.vendors.stuck()
			<-r.Context().Done()
		})
	default:
		v.setAnswer(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
	}
	return nil
}

// listen puts a raw TCP listener on the Talon.One https address; serve
// handles each connection, which is then closed.
func (sut *vendorsSUT) listen(serve func(net.Conn)) {
	l := listenLocal()
	sut.routeTo("api.talon.one", "443", l)
	sut.proxy.opened = append(sut.proxy.opened, l)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				serve(c)
				_, _ = io.Copy(io.Discard, c)
			}()
		}
	}()
}

// stream is what the streaming scenario saw.
type stream struct {
	bodyEarly     atomic.Bool
	responseEarly atomic.Bool
}

// streamBothWays sends a large body in two parts and has the vendor answer
// in two parts. Each side sends its second part only after the other side
// got the first, so a proxy that buffers whole bodies makes it stall. The
// response has a Content-Length and a small first part, so it also stalls when
// the proxy holds small writes back instead of passing them on at once.
func (sut *forwardSUT) streamBothWays() error {
	gotBody := make(chan struct{})
	gotResponse := make(chan struct{})
	part := bytes.Repeat([]byte("x"), 256*1024)
	first := []byte("first part of the response")
	sut.vendors.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
		_, err := io.ReadFull(r.Body, make([]byte, len(part)))
		if err == nil {
			close(gotBody)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		rw.Header().Set("Content-Length", strconv.Itoa(len(first)+len(part)))
		_, _ = rw.Write(first)
		rw.(http.Flusher).Flush()
		select {
		case <-gotResponse:
			sut.stream.responseEarly.Store(true)
		case <-time.After(5 * time.Second):
		}
		_, _ = rw.Write(part)
	})
	body, sendBody := io.Pipe()
	go func() {
		_, _ = sendBody.Write(part)
		select {
		case <-gotBody:
			sut.stream.bodyEarly.Store(true)
		case <-time.After(5 * time.Second):
		}
		_, _ = sendBody.Write(part)
		_ = sendBody.Close()
	}()
	if err := sut.proxy.start(); err != nil {
		return err
	}
	sut.vendors.vendor("api.talon.one")
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", sut.proxy.addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, "https://api.talon.one/v2/customer_sessions/1", body)
	req.Header.Set("Proxy-Authorization", basic("talon-shop-a:x")[len("Proxy-Authorization: "):])
	go func() { _ = req.WriteProxy(conn) }()
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.ReadFull(resp.Body, make([]byte, len(first))); err != nil {
		return err
	}
	close(gotResponse)
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// vendorTLS is a TLS setup with a trusted certificate for host.
func (sut *forwardSUT) vendorTLS(host string) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{sut.vendors.authority.leaf(host, time.Now().Add(24*time.Hour), false)}}
}

// forwardFull sends a call with an ID, secrets, hop headers and a body. A
// target that is a path is meant for the proxy itself, so its Host is the
// proxy's address.
func (sut *forwardSUT) forwardFull(target string) error {
	return sut.forwardWithHost(target, "")
}

// forwardWithHost makes the full app call, with the Host header given, or
// with the proxy's own address for a vendor URL in path call.
func (sut *forwardSUT) forwardWithHost(target, host string) error {
	body := `{"customerSession":{"state":"open"}}`
	headers := []string{
		basic("talon-shop-a:x"),
		"Authorization: ApiKey-v1 c2VjcmV0",
		"Connection: X-Hop",
		"X-Hop: 1",
		"Proxy-Connection: keep-alive",
		"Content-Type: application/json",
		"X-Trace: t-1",
	}
	switch {
	case host != "":
		headers = append(headers, "Host: "+host)
	case strings.HasPrefix(target, "/"):
		headers = append(headers, "Host: localhost:8080")
	}
	return sut.app.send(request{method: "PUT", target: target, headers: headers, body: strings.NewReader(body), length: len(body)})
}

// websocketOnce opens a WebSocket to target through the proxy, reads
// message and closes at once, as the vendor closes its side.
func (sut *forwardSUT) websocketOnce(target, message string, headers ...string) error {
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", sut.proxy.addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	host := hostOf(target)
	_, err = io.WriteString(conn, "GET "+target+" HTTP/1.1\r\nHost: "+host+"\r\n"+
		basic("talon-shop-a:x")+"\r\nAuthorization: Bearer t0k3n\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+
		strings.Join(append(headers, ""), "\r\n")+"\r\n")
	if err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return failf("the app got %d", resp.StatusCode)
	}
	got := make([]byte, len(message))
	if _, err := io.ReadFull(reader, got); err != nil || string(got) != message {
		return failf("the app got %q, %v", got, err)
	}
	return nil
}

// websocketSpec is a shop api whose GET /orders switches to a WebSocket.
const websocketSpec = `openapi: 3.2.0
info: { title: shop live orders, version: "1" }
paths:
  /orders:
    get:
      responses:
        "101": { description: switched to a WebSocket }
`

// forwardSUT is the forward proxy ingress: apps that send their calls to the
// proxy port, and how the proxy forwards them to the vendor.
type forwardSUT struct {
	proxy      *proxySUT
	vendors    *vendorsSUT
	app        *appSUT
	apis       *apisSUT
	otlp       *otlpSUT
	tlsIngress *tlsIngressSUT
	// stream is the app's and the vendor's ends of a call that streams both
	// ways.
	stream *stream
}

func newForwardSUT(root *proxySUT, vendors *vendorsSUT, app *appSUT, apis *apisSUT, otlp *otlpSUT, tlsIngress *tlsIngressSUT) *forwardSUT {
	return &forwardSUT{proxy: root, vendors: vendors, app: app, apis: apis, otlp: otlp, tlsIngress: tlsIngress, stream: &stream{}}
}

// AddSteps adds the steps of the forward proxy ingress.
func (sut *forwardSUT) AddSteps(sc *godog.ScenarioContext) {
	ingressSteps(sc, sut)
	integrationSteps(sc, sut)
	guardSteps(sc, sut)
	hostHeaderSteps(sc, sut)
	forwardSteps(sc, sut)
	trailerSteps(sc, sut)
	timingSteps(sc, sut)
}
