package proxy_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	proxy "hotline-proxy"
	"hotline-proxy/internal/fuzzlog"
)

// TestMain keeps each fuzz worker's output in a file, so a crash says why
// (internal/fuzzlog). It does nothing outside a fuzz worker.
func TestMain(m *testing.M) {
	fuzzlog.Capture()
	os.Exit(m.Run())
}

// The fuzz tests make up input after input and send each one to the proxy
// the way an app or a vendor would, through the proxy's ways in only
// (docs/decisions/fuzzing.md). Three rules hold for every input: the proxy
// never panics, it answers within fuzzWait, and a normal call still works
// after it.
//
// go test runs only the saved inputs: the seeds below and the files in
// testdata/fuzz. The nightly workflow fuzz.yml makes new ones, for example
// go test -run '^$' -fuzz '^FuzzFirstBytes$' -fuzztime 5m .

// fuzzWait is the longest the proxy may take over one input.
const fuzzWait = 2 * time.Second

// fuzzWorld starts the proxy for one fuzz test, with what setup adds.
func fuzzWorld(f *testing.F, setup func(w *world)) *world {
	f.Helper()
	w, err := newWorld(newReservedPorts())
	if err != nil {
		setupFailed(f, err)
	}
	f.Cleanup(w.close)
	w.checkWorkers = 1
	setup(w)
	if err := w.start(); err != nil {
		setupFailed(f, err)
	}
	// A vendor for the normal call each input is followed by.
	w.vendor("api.talon.one")
	return w
}

// setupFailed fails the fuzz test over an error in its setup. Inside a fuzz
// worker Go throws away what f.Fatal says, so the error is also written to
// standard error, which the worker's log keeps (internal/fuzzlog). The
// workflow looks for "fuzz setup failed:" there (nightly fuzz run 11,
// 2026-11-25, left an empty log).
func setupFailed(f *testing.F, err error) {
	f.Helper()
	_, _ = fmt.Fprintln(os.Stderr, "fuzz setup failed:", err)
	f.Fatal(err)
}

// tryTwice sends one input with send, which says how long the proxy took
// over it, and checks the three rules. An input that takes too long is sent
// once more, and only fails when it is slow again: on a busy machine one
// slow moment is not the input's fault, but an input that always takes
// long is.
func (w *world) tryTwice(t *testing.T, send func() time.Duration) {
	t.Helper()
	took := send()
	if took > fuzzWait {
		took = send()
	}
	w.stillWell(t, took)
}

// stillWell checks the three rules after one input that took took.
func (w *world) stillWell(t *testing.T, took time.Duration) {
	t.Helper()
	if took > fuzzWait {
		t.Fatalf("the proxy took %v over the input, more than %v\n%s", took, fuzzWait, w.logs.String())
	}
	// Go's HTTP server writes this line when it stops one call on a panic.
	// A panic anywhere else stops the whole test. Matching only this line
	// keeps a made-up host or path with the word "panic" in it, which the
	// logs may repeat, from failing the test.
	if logs := w.logs.String(); strings.Contains(logs, "http: panic serving") {
		t.Fatalf("the proxy panicked over the input:\n%s", logs)
	}
	w.logs.Reset()
	w.forget()
	w.resp = nil
	if err := w.call("GET", "https://api.talon.one/v1/status"); err != nil || w.resp == nil {
		t.Fatalf("a normal call after the input got no answer: %v %v\n%s", err, w.respErr, w.logs.String())
	}
}

// forget drops what the fakes kept about the calls so far. A fuzz run sends
// hundreds of thousands of inputs, and keeping them all filled the memory
// until the machine slowed down so much that the proxy seemed slow
// (fuzz run of 2026-11-18).
func (w *world) forget() {
	for _, v := range w.vendors {
		v.forget()
	}
	for _, r := range append(w.receivers, w.telemetry) {
		if r != nil {
			r.forget()
		}
	}
}

// sendRaw writes data on a new connection to address, says nothing more
// comes, and reads until the proxy closes the connection. It gives how long
// that took; a wait cut off at fuzzWait counts as longer, the connect's own
// wait included. A connect slow once on a busy machine is then tried again
// like a slow answer, instead of failing at once (fuzz run of 2026-11-21).
func sendRaw(t *testing.T, network, address string, data []byte) time.Duration {
	t.Helper()
	start := time.Now()
	conn, err := (&net.Dialer{Timeout: fuzzWait}).DialContext(context.Background(), network, address)
	if isTimeout(err) {
		return fuzzWait + time.Millisecond
	}
	if err != nil {
		t.Fatalf("cannot connect to the proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(start.Add(fuzzWait))
	_, _ = conn.Write(data)
	if half, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = half.CloseWrite()
	}
	if _, err := io.Copy(io.Discard, conn); isTimeout(err) {
		return fuzzWait + time.Millisecond
	}
	return time.Since(start)
}

func isTimeout(err error) bool {
	var ne net.Error
	return err != nil && errors.As(err, &ne) && ne.Timeout()
}

// clientHello is the first message a real TLS client sends, naming host.
func clientHello(host string) []byte {
	app, proxySide := net.Pipe()
	go func() {
		_ = tls.Client(app, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}).HandshakeContext(context.Background())
	}()
	_ = proxySide.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1<<14)
	n, _ := proxySide.Read(buf)
	_ = proxySide.Close()
	_ = app.Close()
	return buf[:n]
}

// FuzzFirstBytes sends the first bytes of a program's connection to the
// transparent listener, and the same bytes after a capture CONNECT on the
// capture socket file: the TLS hello, or whatever stands in its place.
func FuzzFirstBytes(f *testing.F) {
	w := fuzzWorld(f, func(w *world) {
		w.useAuthority(newAuthority())
		w.transparent = &transparentConf{authority: "customer"}
		w.captureFile = filepath.Join(w.dir, "capture.sock")
	})
	f.Add(clientHello("api.talon.one"))
	f.Add(clientHello(""))
	f.Add([]byte("GET / HTTP/1.1\r\nHost: api.talon.one\r\n\r\n"))
	f.Add([]byte{0x16, 3, 1, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		w.tryTwice(t, func() time.Duration { return sendRaw(t, "tcp", w.transparentAddr, data) })
		connect := "CONNECT api.talon.one:443 HTTP/1.1\r\nHost: api.talon.one:443\r\n" +
			"Hotline-Capture: 2\r\nHotline-Capture-Pid: 42\r\nHotline-Capture-Program: /usr/bin/app\r\n\r\n"
		w.tryTwice(t, func() time.Duration { return sendRaw(t, "unix", w.captureFile, append([]byte(connect), data...)) })
	})
}

// FuzzRequest sends a made-up request on the proxy port: its request line,
// Host, Proxy-Authorization and the rest, CONNECT included.
func FuzzRequest(f *testing.F) {
	w := fuzzWorld(f, func(w *world) {
		w.useAuthority(newAuthority())
		w.listenConnect = "  connect: { cert: customer }\n"
	})
	login := basic("talon-shop-a:x")
	f.Add([]byte("GET https://api.talon.one/v1/status HTTP/1.1\r\nHost: api.talon.one\r\n" + login + "\r\n\r\n"))
	f.Add([]byte("GET /https://api.talon.one/v1/status HTTP/1.1\r\nHost: localhost\r\n\r\n"))
	f.Add([]byte("POST /v1/status HTTP/1.1\r\nHost: api.talon.one\r\nContent-Length: 2\r\n\r\n{}"))
	f.Add([]byte("CONNECT api.talon.one:443 HTTP/1.1\r\nHost: api.talon.one:443\r\n" + login + "\r\n\r\n"))
	f.Add([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		w.tryTwice(t, func() time.Duration { return sendRaw(t, "tcp", w.addr, data) })
	})
}

// FuzzRequestBody sends a made-up body to an api whose spec checks it: JSON,
// a form as Stripe writes it, or XML. The call's record must arrive in
// time, after its check.
func FuzzRequestBody(f *testing.F) {
	w := fuzzWorld(f, func(w *world) {
		shop, err := os.ReadFile(filepath.Join("testdata", "checks", "shop.openapi.yaml"))
		if err != nil {
			f.Fatal(err)
		}
		files, err := os.ReadFile(filepath.Join("testdata", "xml", "files.openapi.yaml"))
		if err != nil {
			f.Fatal(err)
		}
		w.addIntegration(integration("talon-shop-a",
			w.specAPI("api.shop.test", string(shop), ""), w.specAPI(filesHost, string(files), "")))
	})
	w.filesAnswer(200, "application/xml", "<DeleteResult/>")
	f.Add(uint8(0), []byte(`{"item":"A1","quantity":1,"lines":[{"sku":"s"}]}`))
	f.Add(uint8(1), []byte("amount=5&currency=eur&metadata[order]=6735&items[0][price]=p1&items[0][quantity]=2"))
	f.Add(uint8(2), []byte(`<Delete><Quiet>true</Quiet><Object><Key>a</Key></Object></Delete>`))
	f.Fuzz(func(t *testing.T, kind uint8, body []byte) {
		w.tryTwice(t, func() time.Duration {
			start := time.Now()
			w.sendBody(kind, body)
			return w.recordTime(start)
		})
	})
}

// sendBody sends body to the api the kind picks: JSON, a form or XML.
func (w *world) sendBody(kind uint8, body []byte) {
	switch kind % 3 {
	case 0:
		_ = w.shopCall("POST", "/orders", true, "Idempotency-Key: k1", "json", string(body))
	case 1:
		_ = w.shopCall("POST", "/payments", true, "", "form", string(body))
	default:
		_ = w.send(request{
			method: "POST", target: "https://" + filesHost + "/delete", headers: []string{basic("talon-shop-a:x"), "Content-Type: application/xml"},
			body: bytes.NewReader(body), length: len(body),
		})
	}
}

// FuzzResponse has the vendor answer with a made-up content type and body,
// which the proxy checks against the spec. The record must arrive in time.
func FuzzResponse(f *testing.F) {
	w := fuzzWorld(f, func(w *world) {
		files, err := os.ReadFile(filepath.Join("testdata", "xml", "files.openapi.yaml"))
		if err != nil {
			f.Fatal(err)
		}
		w.addIntegration(integration("talon-shop-a", w.specAPI(filesHost, string(files), "")))
	})
	f.Add("application/xml", []byte(`<ListBucketResult><Name>shop-invoices</Name><KeyCount>1</KeyCount><Contents><Key>a.pdf</Key><Size>1</Size></Contents></ListBucketResult>`))
	f.Add("application/xml", []byte(`<Error><Code>NoSuchBucket</Code></Error>`))
	f.Add("application/json", []byte(`{"Name":"shop-invoices"}`))
	f.Fuzz(func(t *testing.T, mediaType string, body []byte) {
		w.filesAnswer(200, mediaType, string(body))
		w.tryTwice(t, func() time.Duration {
			start := time.Now()
			_ = w.send(request{method: "GET", target: "https://" + filesHost + "/?list-type=2", headers: []string{basic("talon-shop-a:x")}, length: -1})
			return w.recordTime(start)
		})
	})
}

// FuzzStreamedResponse has the fake Anthropic stream made-up bytes as
// Server-Sent Events, in two pieces cut at a made-up place, so an event,
// a line end or a byte order mark may be split. Besides the three rules,
// the stream's record must arrive within fuzzWait, its items' checks
// included.
func FuzzStreamedResponse(f *testing.F) {
	w := fuzzWorld(f, func(w *world) {
		spec, err := os.ReadFile(filepath.FromSlash(anthropicSpec))
		if err != nil {
			f.Fatal(err)
		}
		w.streaming = &streamingVendor{spec: string(spec), at: len(w.integrations)}
		w.addIntegration("")
		w.writeStreamingSpec()
		w.vendor(anthropicHost).setAnswer(w.streamingVendor)
	})
	f.Add(bytes.Join(helloResponse(), nil), uint16(7))
	f.Add([]byte("\xef\xbb\xbfevent: ping\r\ndata: {\"type\":\"ping\"}\r\n\r\n"), uint16(2))
	f.Add([]byte("retry: +5\rid: a\x00b\rdata\rdata:\r\r: comment\n\nevent: ping\ndata: {}"), uint16(9))
	f.Fuzz(func(t *testing.T, stream []byte, cut uint16) {
		at := 0
		if len(stream) > 0 {
			at = int(cut) % len(stream)
		}
		w.streaming.mu.Lock()
		w.streaming.events = [][]byte{stream[:at], stream[at:]}
		w.streaming.mu.Unlock()
		w.tryTwice(t, func() time.Duration {
			start := time.Now()
			streams := len(w.receivers[0].streamRecords())
			_ = w.send(anthropicRequest(true))
			took := w.recordTime(start)
			for len(w.receivers[0].streamRecords()) <= streams && time.Since(start) <= fuzzWait {
				pause(time.Millisecond)
			}
			return max(took, time.Since(start))
		})
	})
}

// recordTime is how long after start the record of every call sent so far
// arrived, its spec check included, counting the records forget dropped; a
// wait cut off at fuzzWait counts as longer.
func (w *world) recordTime(start time.Time) time.Duration {
	for {
		if w.receivers[0].takenCalls() >= w.sent {
			return time.Since(start)
		}
		if time.Since(start) > fuzzWait {
			return fuzzWait + time.Millisecond
		}
		pause(time.Millisecond)
	}
}

// FuzzVerify has hotline-proxy verify read a made-up file of records, as
// it is and gzipped.
func FuzzVerify(f *testing.F) {
	f.Add([]byte(`{"time":"2026-11-18T07:00:00Z","event":"hotline.request","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7","attributes":{"hotline.integration.id":"talon-shop-a","hotline.batch.index":0}}` + "\n"))
	f.Add([]byte(`{"time":"2026-11-18T07:00:01Z","event":"hotline.ledger.batch","attributes":{"hotline.batch.format":"hotline.batch.v3","hotline.batch.id":"b1","hotline.batch.integration":"talon-shop-a","hotline.batch.key":"ed25519:AAAA","hotline.batch.signature":"ed25519:AAAA"}}` + "\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		var zipped bytes.Buffer
		zw := gzip.NewWriter(&zipped)
		_, _ = zw.Write(data)
		_ = zw.Close()
		if os.WriteFile(filepath.Join(dir, "calls.jsonl"), data, 0o600) != nil ||
			os.WriteFile(filepath.Join(dir, "calls-20261118T070000Z.jsonl.gz"), zipped.Bytes(), 0o600) != nil {
			t.Fatal("cannot write the files to verify")
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			proxy.Verify(io.Discard, proxy.VerifyOptions{Folder: dir})
		}()
		select {
		case <-done:
		case <-time.After(fuzzWait):
			t.Fatalf("verify did not finish within %v", fuzzWait)
		}
	})
}
