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

// trailerSteps cover what comes after a body, and early replies.
func trailerSteps(sc *godog.ScenarioContext, w *world) {
	var vendorTrailer chan http.Header
	sc.Step(`^a vendor that reads the trailers of the request$`, func() error {
		vendorTrailer = make(chan http.Header, 1)
		got := vendorTrailer
		w.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			got <- r.Trailer.Clone()
		})
		return nil
	})
	sc.Step(`^an app sends a body in parts with the trailer "([^"]+)"$`, func(trailer string) error {
		return w.send(request{
			method:   "PUT",
			target:   w.target,
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
			return failf("the vendor got no call: the app got %v (%v)", w.resp, w.respErr)
		}
	})

	sc.Step(`^a vendor that ends its reply with the trailers "([^"]+)", "([^"]+)" and "([^"]+)"$`, func(a, b, c string) error {
		// Over HTTP/1.1, as Go's HTTP/2 server will not send forbidden
		// trailers at all.
		v := w.vendor("api.talon.one")
		s := httptest.NewUnstartedServer(v.record("https", "443"))
		s.TLS = w.vendorTLS("api.talon.one")
		s.TLS.NextProtos = []string{"http/1.1"}
		s.StartTLS()
		w.routeTo("api.talon.one", "443", s.Listener)
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
		if w.resp == nil || w.resp.Trailer.Get(name) != value {
			return failf("the app got %v", w.resp)
		}
		return nil
	})
	sc.Step(`^the app gets no trailer (\S+) or (\S+)$`, func(a, b string) error {
		for _, name := range []string{a, b} {
			if _, ok := w.resp.Trailer[http.CanonicalHeaderKey(name)]; ok {
				return failf("the app got the trailer %s: %v", name, w.resp.Trailer)
			}
		}
		return nil
	})

	// replies are the status and headers of each reply the app got.
	type reply struct {
		status int
		header http.Header
	}
	var replies []reply
	sc.Step(`^a vendor that sends 103 Early Hints with a Link header and Connection, X-Nominated and Keep-Alive headers, then 200$`, func() error {
		w.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
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
	sc.Step(`^an app calls it through the proxy and reads every reply$`, func() error {
		if err := w.start(); err != nil {
			return err
		}
		w.vendor("api.talon.one")
		conn, err := w.dialProxy()
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		_, err = io.WriteString(conn, "GET "+w.target+" HTTP/1.1\r\nHost: api.talon.one\r\n"+basic("talon-shop-a:x")+"\r\n\r\n")
		if err != nil {
			return err
		}
		reader := bufio.NewReader(conn)
		replies = nil
		for {
			resp, err := http.ReadResponse(reader, nil)
			if err != nil {
				return err
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			replies = append(replies, reply{resp.StatusCode, resp.Header})
			if resp.StatusCode >= 200 {
				return nil
			}
		}
	})
	sc.Step(`^the app gets 103 with the Link header but no Connection, X-Nominated or Keep-Alive header$`, func() error {
		if len(replies) < 2 || replies[0].status != http.StatusEarlyHints {
			return failf("the app got %d replies, the first %v", len(replies), replies)
		}
		early := replies[0].header
		if early.Get("Link") == "" {
			return failf("the early reply lost its Link header: %v", early)
		}
		for _, name := range []string{"Connection", "X-Nominated", "Keep-Alive"} {
			if early.Get(name) != "" {
				return failf("the early reply kept %s: %v", name, early)
			}
		}
		return nil
	})
	sc.Step(`^then the app gets (\d+)$`, func(status int) error {
		last := replies[len(replies)-1]
		if last.status != status {
			return failf("the app's last reply was %d", last.status)
		}
		return nil
	})
}
