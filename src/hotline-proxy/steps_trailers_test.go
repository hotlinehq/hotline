package proxy_test

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// trailerSteps cover what comes after a body, and early responses.
func trailerSteps(sc *godog.ScenarioContext, sut *forwardSUT) {
	var vendorTrailer chan http.Header
	sc.Step(`^a vendor that reads the trailers of the request$`, func() error {
		vendorTrailer = make(chan http.Header, 1)
		got := vendorTrailer
		sut.vendors.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			got <- r.Trailer.Clone()
		})
		return nil
	})
	sc.Step(`^an app sends a body in parts with the trailer "([^"]+)"$`, func(trailer string) error {
		return sut.app.send(request{
			method:   "PUT",
			target:   sut.app.target,
			headers:  []string{basic("talon-shop-a:x")},
			body:     bytes.NewReader([]byte(`{"part":1}`)),
			length:   -1,
			chunked:  true,
			trailers: []string{trailer},
		})
	})
	sc.Step(`^the vendor gets the trailer "([^"]+)"$`, func(trailer string) error {
		name, value, _ := strings.Cut(trailer, ": ")
		select {
		case got := <-vendorTrailer:
			if got.Get(name) != value {
				return failf("the vendor got the trailers %v", got)
			}
			return nil
		case <-time.After(5 * time.Second):
			return failf("the vendor got no call: the app got %v (%v)", sut.app.resp, sut.app.respErr)
		}
	})

	sc.Step(`^a vendor that ends its response with the trailers "([^"]+)", "([^"]+)" and "([^"]+)"$`, func(a, b, c string) error {
		// Over HTTP/1.1, as Go's HTTP/2 server will not send forbidden
		// trailers at all.
		v := sut.vendors.vendor("api.talon.one")
		s := httptest.NewUnstartedServer(v.record("https", "443"))
		s.TLS = sut.vendorTLS("api.talon.one")
		s.TLS.NextProtos = []string{"http/1.1"}
		s.StartTLS()
		sut.vendors.routeTo("api.talon.one", "443", s.Listener)
		v.servers = append(v.servers, s)
		v.setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(rw, "messages")
			rw.(http.Flusher).Flush()
			for _, t := range []string{a, b, c} {
				name, value, _ := strings.Cut(t, ": ")
				rw.Header().Set(http.TrailerPrefix+name, value)
			}
		})
		return nil
	})
	sc.Step(`^the app gets the trailer "([^"]+)"$`, func(trailer string) error {
		name, value, _ := strings.Cut(trailer, ": ")
		if sut.app.resp == nil || sut.app.resp.Trailer.Get(name) != value {
			return failf("the app got %v", sut.app.resp)
		}
		return nil
	})
	sc.Step(`^the app gets no trailer (\S+) or (\S+)$`, func(a, b string) error {
		for _, name := range []string{a, b} {
			if _, ok := sut.app.resp.Trailer[http.CanonicalHeaderKey(name)]; ok {
				return failf("the app got the trailer %s: %v", name, sut.app.resp.Trailer)
			}
		}
		return nil
	})

	// responses are the status and headers of each response the app got.
	type response struct {
		status int
		header http.Header
	}
	var responses []response
	sc.Step(`^a vendor that sends 103 Early Hints with a Link header and Connection, X-Nominated and Keep-Alive headers, then 200$`, func() error {
		sut.vendors.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			h := rw.Header()
			h.Set("Link", "</style.css>; rel=preload")
			h.Set("Connection", "X-Nominated")
			h.Set("X-Nominated", "for this hop")
			h.Set("Keep-Alive", "timeout=5")
			rw.WriteHeader(http.StatusEarlyHints)
			for name := range h {
				h.Del(name)
			}
			_, _ = io.WriteString(rw, "final")
		})
		return nil
	})
	sc.Step(`^an app calls it through the proxy and reads every response$`, func() error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		sut.vendors.vendor("api.talon.one")
		conn, err := sut.app.dialProxy()
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		_, err = io.WriteString(conn, "GET "+sut.app.target+" HTTP/1.1\r\nHost: api.talon.one\r\n"+basic("talon-shop-a:x")+"\r\n\r\n")
		if err != nil {
			return err
		}
		reader := bufio.NewReader(conn)
		responses = nil
		for {
			resp, err := http.ReadResponse(reader, nil)
			if err != nil {
				return err
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			responses = append(responses, response{resp.StatusCode, resp.Header})
			if resp.StatusCode >= 200 {
				return nil
			}
		}
	})
	sc.Step(`^the app gets 103 with the Link header but no Connection, X-Nominated or Keep-Alive header$`, func() error {
		if len(responses) < 2 || responses[0].status != http.StatusEarlyHints {
			return failf("the app got %d responses, the first %v", len(responses), responses)
		}
		early := responses[0].header
		if early.Get("Link") == "" {
			return failf("the early response lost its Link header: %v", early)
		}
		for _, name := range []string{"Connection", "X-Nominated", "Keep-Alive"} {
			if early.Get(name) != "" {
				return failf("the early response kept %s: %v", name, early)
			}
		}
		return nil
	})
	sc.Step(`^then the app gets (\d+)$`, func(status int) error {
		last := responses[len(responses)-1]
		if last.status != status {
			return failf("the app's last response was %d", last.status)
		}
		return nil
	})
}
