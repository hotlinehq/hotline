package proxy_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

func hostHeaderSteps(sc *godog.ScenarioContext, sut *forwardSUT) {
	sc.Step(`^the call uses the Host header ingress$`, func() error {
		return sut.otlp.lastHas("hotline.ingress", "host_header")
	})
	sc.Step(`^the proxy forwards it to "([^"]+)" with "Host: (\S+)"$`, sut.forwardWithHost)
	sc.Step(`^an app sends "([A-Z]+) ([^"]+)" over HTTP/1.0 with no Host header$`, func(method, path string) error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		conn, err := sut.app.dialProxy()
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		if _, err := io.WriteString(conn, method+" "+path+" HTTP/1.0\r\n"+basic(sut.app.appID+":x")+"\r\n\r\n"); err != nil {
			return err
		}
		return sut.app.readAnswer(conn)
	})
	sc.Step(`^an app sends a Host header call to the proxy's own address$`, func() error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		return sut.app.call("GET", "/v2/x", "Host: "+sut.proxy.addr)
	})
	sc.Step(`^an app sends "([A-Z]+) ([^"]+)" with "Host: (\S+)" and "Upgrade: h2c"$`, func(method, path, host string) error {
		return sut.app.call(method, path, "Host: "+host, "Connection: Upgrade, HTTP2-Settings", "Upgrade: h2c", "HTTP2-Settings: AAMAAABkAAQCAAAAAAIAAAAA")
	})
	sc.Step(`^the connection stays HTTP/1.1$`, func() error {
		if sut.app.resp == nil || sut.app.resp.Proto != "HTTP/1.1" || sut.app.resp.StatusCode != http.StatusOK {
			return failf("the app got %v", sut.app.resp)
		}
		return nil
	})

	sc.Step(`^an app that speaks HTTP/2 straight away without TLS$`, func() error { return nil })
	sc.Step(`^it sends "([A-Z]+) ([^"]+)" with ":authority (\S+)"(?: and ":scheme http")?$`, func(method, path, authority string) error {
		return sut.app.sendHTTP2(method, path, authority)
	})
	sc.Step(`^the app sends "([A-Z]+) ([^"]+)" with ":authority (\S+)" over TLS to the proxy$`, sut.app.sendHTTP2)
	sc.Step(`^the connection uses HTTP/2$`, func() error {
		if sut.app.tlsState.NegotiatedProtocol != "h2" || sut.app.resp == nil || sut.app.resp.ProtoMajor != 2 {
			return failf("the connection agreed on %q and the app got %v", sut.app.tlsState.NegotiatedProtocol, sut.app.resp)
		}
		return nil
	})
}

// sendHTTP2 sends one HTTP/2 call to the proxy, over TLS when the app uses
// it and otherwise plain from the first byte, with :authority set.
func (sut *appSUT) sendHTTP2(method, path, authority string) error {
	if err := sut.proxy.start(); err != nil {
		return err
	}
	host := strings.ToLower(authority)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if _, err := netip.ParseAddr(host); err != nil && !strings.HasPrefix(path, "/http") {
		sut.vendors.vendor(host)
	}
	if strings.HasPrefix(path, "/http") {
		sut.vendors.vendor(strings.ToLower(hostOf(path)))
	}
	protocols := new(http.Protocols)
	transport := &http.Transport{Protocols: protocols}
	scheme := "http"
	if sut.appTLS != nil {
		protocols.SetHTTP1(true)
		protocols.SetHTTP2(true)
		transport.TLSClientConfig = sut.appTLS
		scheme = "https"
	} else {
		protocols.SetUnencryptedHTTP2(true)
	}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, scheme+"://"+sut.proxy.addr+path, nil)
	if err != nil {
		return err
	}
	req.Host = authority
	if sut.appID != "" {
		req.Header.Set("Proxy-Authorization", strings.TrimPrefix(basic(sut.appID+":x"), "Proxy-Authorization: "))
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	sut.resp, sut.respErr = resp, err
	if err != nil {
		return nil
	}
	if resp.TLS != nil {
		sut.tlsState = *resp.TLS
	}
	sut.respBody, sut.respErr = io.ReadAll(resp.Body)
	return resp.Body.Close()
}
