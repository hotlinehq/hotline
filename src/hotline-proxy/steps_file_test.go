package proxy_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/cucumber/godog"

	proxy "hotline-proxy"
)

// fileExporter is one file exporter of the config: its folder, and more
// lines for it.
type fileExporter struct {
	path  string
	extra string
}

func fileSteps(sc *godog.ScenarioContext, w *world) {
	var secondErr error
	sc.Step(`^a file exporter$`, func() error {
		w.files = append(w.files, fileExporter{path: filepath.Join(w.dir, "calls")})
		return nil
	})
	sc.Step(`^no file exporter$`, func() error {
		w.files = nil
		return nil
	})
	sc.Step(`^no OTLP exporter$`, func() error {
		for _, r := range w.receivers {
			r.stop()
		}
		w.receivers = nil
		return nil
	})
	sc.Step(`^the file exporter has maxFileSize (\S+) and maxFiles (\d+)$`, func(size, count string) error {
		w.files[0].extra = "      maxFileSize: " + size + "\n      maxFiles: " + count + "\n"
		return nil
	})
	sc.Step(`^the file exporter has maxFileSize (\S+), maxFiles (\d+) and compression none$`, func(size, count string) error {
		w.files[0].extra = "      maxFileSize: " + size + "\n      maxFiles: " + count + "\n      compression: none\n"
		return nil
	})
	sc.Step(`^the file exporter has queueMemory (\S+)$`, func(size string) error {
		w.files[0].extra = "      queueMemory: " + size + "\n"
		return nil
	})
	sc.Step(`^the file exporter's path is a file, not a folder$`, func() error {
		w.files[0].path = w.writeFile("calls", "not a folder\n")
		return nil
	})
	sc.Step(`^a second file exporter on the same folder$`, func() error {
		w.files = append(w.files, w.files[0])
		return nil
	})
	sc.Step(`^a second file exporter in its own folder$`, func() error {
		w.files = append(w.files, fileExporter{path: filepath.Join(w.dir, "calls-2")})
		return nil
	})
	sc.Step(`^the integration has (content|maxBodySize) (\S+)$`, func(name, value string) error {
		w.addIntegration(integration("talon-shop-a", w.talonAPI(""), w.stripeAPI("")) + "    " + name + ": " + value + "\n")
		return nil
	})
	sc.Step(`^the integration has content full with maxBodySize (\S+)$`, func(size string) error {
		w.addIntegration(integration("talon-shop-a", w.talonAPI(""), w.stripeAPI("")) + "    content: full\n    maxBodySize: " + size + "\n")
		return nil
	})
	sc.Step(`^the vendor replies with a 5KiB body$`, func() error {
		w.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			rw.Header().Set("Content-Type", "text/plain")
			_, _ = rw.Write(longReply())
		})
		return nil
	})
	sc.Step(`^10 calls end$`, func() error {
		for range 10 {
			if err := w.defaultCall("Content-Type: application/json"); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^a call with a 4KiB request body ends$`, func() error {
		return w.callWithBody(strings.Repeat("x", 4096), "Content-Type: text/plain")
	})
	sc.Step(`^a call with a JSON body and an Authorization header ends$`, func() error {
		return w.callWithBody(`{"name":"Ana \"A\"","items":[1,2]}`, "Content-Type: application/json", "Authorization: Bearer secret-token-1")
	})
	sc.Step(`^a call with a Proxy-Trace header ends$`, func() error {
		return w.defaultCall("Content-Type: application/json", "Proxy-Trace: trace-7")
	})
	sc.Step(`^a call to a host without an api ends$`, func() error {
		w.target = "https://api.other.test/v1/things?q=1"
		return w.callWithBody(`{"thing":1}`, "Content-Type: application/json")
	})
	sc.Step(`^the proxy runs and a call ended$`, func() error {
		if err := w.defaultCall("Content-Type: application/json"); err != nil {
			return err
		}
		return w.waitForLines(1)
	})
	sc.Step(`^the folder is deleted and another call ends$`, func() error {
		if err := os.RemoveAll(w.files[0].path); err != nil {
			return err
		}
		return w.defaultCall("Content-Type: application/json")
	})
	sc.Step(`^a second proxy starts with a file exporter on the same folder$`, func() error {
		config := strings.Replace(w.config(), w.addr, freePort(), 1)
		second, err := proxy.Start(context.Background(), proxy.Options{
			ConfigPath:    w.writeFile("second.yaml", config),
			Resolver:      w.network,
			Dialer:        w.network,
			RootCAs:       w.issuer.pool,
			Clock:         proxy.SystemClock{},
			Logger:        slog.New(slog.DiscardHandler),
			ExportTimeout: time.Second,
			Version:       "1.0.0-test",
			InstanceID:    "proxy-test-2",
			LookupEnv:     func(string) (string, bool) { return "", false },
		})
		secondErr = err
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = second.Shutdown(ctx)
		}
		return nil
	})

	sc.Step(`^calls.jsonl has one line$`, func() error {
		_, err := w.fileLine(0)
		return err
	})
	sc.Step(`^calls.jsonl has no line$`, func() error {
		lines, err := w.fileLines(0)
		if err != nil {
			return err
		}
		if len(lines) > 0 {
			return failf("calls.jsonl has %d lines", len(lines))
		}
		return nil
	})
	sc.Step(`^the line has the time, a severity, event hotline.request, the trace and span ID, and the resource service.name hotline-proxy$`, func() error {
		line, err := w.fileLine(0)
		if err != nil {
			return err
		}
		if _, err := time.Parse(time.RFC3339Nano, sprint(line["time"])); err != nil {
			return failf("the time %v is not an RFC 3339 time", line["time"])
		}
		if !slices.Contains([]string{"INFO", "WARN", "ERROR"}, sprint(line["severity"])) {
			return failf("the severity is %v", line["severity"])
		}
		if err := has(line, "event", "hotline.request"); err != nil {
			return err
		}
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(sprint(line["trace_id"])) {
			return failf("trace_id is %v", line["trace_id"])
		}
		if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(sprint(line["span_id"])) {
			return failf("span_id is %v", line["span_id"])
		}
		resource, _ := line["resource"].(map[string]any)
		return has(resource, "service.name", "hotline-proxy")
	})
	sc.Step(`^the line's attributes have http.request.method PUT and http.response.status_code 200 as a number$`, func() error {
		attrs, err := w.lineAttrs()
		if err != nil {
			return err
		}
		if err := has(attrs, "http.request.method", "PUT"); err != nil {
			return err
		}
		if _, number := attrs["http.response.status_code"].(json.Number); !number {
			return failf("http.response.status_code is %#v, not a number", attrs["http.response.status_code"])
		}
		return has(attrs, "http.response.status_code", 200)
	})
	sc.Step(`^the line's hotline.trace.spans has hotline call with no parent, and vendor call under it$`, func() error {
		attrs, err := w.lineAttrs()
		if err != nil {
			return err
		}
		spans, _ := attrs["hotline.trace.spans"].([]any)
		byName := map[string]map[string]any{}
		for _, s := range spans {
			span, _ := s.(map[string]any)
			byName[sprint(span["name"])] = span
		}
		call, vendor := byName["hotline call"], byName["vendor call"]
		if call == nil || vendor == nil {
			return failf("the spans are %v", spans)
		}
		if call["parent_span_id"] != nil {
			return failf("hotline call has the parent %v", call["parent_span_id"])
		}
		if vendor["parent_span_id"] != call["span_id"] {
			return failf("vendor call's parent is %v, not %v", vendor["parent_span_id"], call["span_id"])
		}
		return has(vendor, "kind", "client")
	})
	sc.Step(`^the folder holds no other file than calls.jsonl$`, func() error {
		w.settle()
		entries, err := os.ReadDir(w.files[0].path)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Name() != "calls.jsonl" && e.Name() != ".lock" {
				return failf("the folder holds %s", e.Name())
			}
		}
		return nil
	})
	sc.Step(`^every attribute the OTLP receiver got is in the line with the same value$`, func() error {
		record, err := w.record()
		if err != nil {
			return err
		}
		attrs, err := w.lineAttrs()
		if err != nil {
			return err
		}
		for key, value := range record {
			if err := has(attrs, key, value); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^calls.jsonl has the line within a second, while the proxy runs$`, func() error {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(filepath.Join(w.files[0].path, "calls.jsonl")); err == nil && bytes.Count(data, []byte("\n")) == 1 {
				return nil
			}
			time.Sleep(10 * time.Millisecond)
		}
		return failf("no line was written within a second")
	})
	sc.Step(`^calls.jsonl is plain text$`, func() error {
		w.settle()
		data, err := os.ReadFile(filepath.Join(w.files[0].path, "calls.jsonl"))
		if err != nil {
			return err
		}
		_, err = parseLines(data)
		return err
	})
	sc.Step(`^there are (\d+) rotated files, each ending in (\S+) and holding only whole lines$`, func(count int, ending string) error {
		w.settle()
		rotated, _ := filepath.Glob(filepath.Join(w.files[0].path, "calls-*"))
		if len(rotated) != count {
			return failf("there are %d rotated files: %v", len(rotated), rotated)
		}
		for _, name := range rotated {
			if !strings.HasSuffix(name, ending) {
				return failf("%s does not end in %s", name, ending)
			}
			data, err := os.ReadFile(name) //nolint:gosec // a test folder
			if err != nil {
				return err
			}
			if strings.HasSuffix(name, ".gz") {
				z, err := gzip.NewReader(bytes.NewReader(data))
				if err != nil {
					return err
				}
				if data, err = io.ReadAll(z); err != nil {
					return err
				}
			}
			lines, err := parseLines(data)
			if err != nil {
				return failf("%s: %v", name, err)
			}
			if len(lines) == 0 {
				return failf("%s holds no line", name)
			}
		}
		return nil
	})
	sc.Step(`^the proxy refuses the config, naming the file exporter$`, func() error {
		return w.refusedWith("file exporter", "cannot be written to")
	})
	sc.Step(`^the proxy refuses the config, saying (.+)$`, func(problem string) error {
		return w.refusedWith(problem)
	})
	sc.Step(`^both folders hold the call's line$`, func() error {
		for i := range w.files {
			if _, err := w.fileLine(i); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the second proxy refuses to start, naming the folder$`, func() error {
		if secondErr == nil {
			return failf("the second proxy started")
		}
		if !strings.Contains(secondErr.Error(), w.files[0].path) || !strings.Contains(secondErr.Error(), "in use") {
			return failf("the second proxy said %q", secondErr)
		}
		return nil
	})
	sc.Step(`^the call succeeds$`, func() error {
		if w.resp == nil || w.resp.StatusCode != http.StatusOK {
			return failf("the app got %v (%v)", w.resp, w.respErr)
		}
		return nil
	})
	sc.Step(`^calls.jsonl is back in the folder with the new call's line$`, func() error {
		_, err := w.fileLine(0)
		return err
	})
	sc.Step(`^the proxy warns that the queue was full$`, func() error {
		w.settle()
		if !strings.Contains(w.logs.String(), "export queue full") {
			return failf("the proxy did not warn: %s", w.logs.String())
		}
		return nil
	})
	sc.Step(`^the line's (\S+) decodes to the exact body sent$`, func(key string) error {
		return w.lineBody(key, w.sentBody)
	})
	sc.Step(`^the line's (\S+) decodes to the vendor's reply$`, func(key string) error {
		return w.lineBody(key, `{"ok":true}`)
	})
	sc.Step(`^the line's hotline.http.response.body holds the first 1KiB$`, func() error {
		return w.lineBody("hotline.http.response.body", string(longReply()[:1024]))
	})
	sc.Step(`^the line has http.request.header.(\S+) as sent$`, func(name string) error {
		attrs, err := w.lineAttrs()
		if err != nil {
			return err
		}
		return has(attrs, "http.request.header."+name, []any{headerValue(w.lastSent, name)})
	})
	sc.Step(`^the line has ([a-z][\w-]*\.[\w.-]+) (\S+)$`, func(key, value string) error {
		attrs, err := w.lineAttrs()
		if err != nil {
			return err
		}
		if list, ok := attrs[key].([]any); ok && len(list) == 1 {
			return has(map[string]any{key: list[0]}, key, value)
		}
		return has(attrs, key, value)
	})
	sc.Step(`^the line has hotline.http.response.body.truncated true and http.response.body.size 5120$`, func() error {
		attrs, err := w.lineAttrs()
		if err != nil {
			return err
		}
		if err := has(attrs, "hotline.http.response.body.truncated", true); err != nil {
			return err
		}
		return has(attrs, "http.response.body.size", 5120)
	})
	sc.Step(`^the line has no (\S+)$`, func(key string) error {
		attrs, err := w.lineAttrs()
		if err != nil {
			return err
		}
		return hasNo(attrs, key)
	})
	sc.Step(`^the line's url.full is the address as sent$`, func() error {
		attrs, err := w.lineAttrs()
		if err != nil {
			return err
		}
		return has(attrs, "url.full", w.target)
	})
	sc.Step(`^the app gets all 5KiB$`, func() error {
		if len(w.respBody) != 5120 {
			return failf("the app got %d bytes (%v)", len(w.respBody), w.respErr)
		}
		return nil
	})
	sc.Step(`^the OTLP record has no hotline.http.request.body and no http.request.header.authorization$`, func() error {
		record, err := w.record()
		if err != nil {
			return err
		}
		return hasNo(record, "hotline.http.request.body", "http.request.header.authorization")
	})
	sc.Step(`^the line's hotline.request.body.signature is the HMAC-SHA256 of the saved body with the integration's key$`, func() error {
		attrs, err := w.lineAttrs()
		if err != nil {
			return err
		}
		saved, err := base64.StdEncoding.DecodeString(sprint(attrs["hotline.http.request.body"]))
		if err != nil {
			return err
		}
		// An integration without a fingerprintKey signs with its ID.
		mac := hmac.New(sha256.New, []byte("talon-shop-a"))
		mac.Write(saved)
		return has(attrs, "hotline.request.body.signature", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	})
	sc.Step(`^the line has the request and reply headers and the reply body$`, func() error {
		attrs, err := w.lineAttrs()
		if err != nil {
			return err
		}
		for key, want := range map[string]any{
			"http.request.header.content-type":  []any{"application/json"},
			"http.response.header.x-request-id": []any{"req-5521"},
		} {
			if err := has(attrs, key, want); err != nil {
				return err
			}
		}
		return w.lineBody("hotline.http.response.body", `{"ok":true}`)
	})
	sc.Step(`^the proxy warns that integration talon-shop-a has content full but there is no file exporter$`, func() error {
		if w.startErr != nil {
			return w.startErr
		}
		if !strings.Contains(w.logs.String(), "integration talon-shop-a has content: full but there is no file exporter") {
			return failf("the proxy did not warn: %s", w.logs.String())
		}
		return nil
	})
}

// callWithBody makes the world's call with a body.
func (w *world) callWithBody(body string, headers ...string) error {
	w.sentBody = body
	if w.appID != "" {
		headers = append([]string{basic(w.appID + ":x")}, headers...)
	}
	return w.send(request{method: w.method, target: w.target, headers: headers, body: strings.NewReader(body), length: len(body)})
}

// longReply is a reply of 5 KiB that is not the same all along, so a cut
// in the wrong place shows.
func longReply() []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < 5120; i++ {
		b.WriteString(strings.Repeat(string(rune('a'+i%26)), 64))
	}
	return b.Bytes()[:5120]
}

// fileLines are the lines of a file exporter's calls.jsonl, once the proxy
// has written everything.
func (w *world) fileLines(i int) ([]map[string]any, error) {
	w.settle()
	data, err := os.ReadFile(filepath.Join(w.files[i].path, "calls.jsonl"))
	if err != nil {
		return nil, err
	}
	return parseLines(data)
}

// fileLine is the only line of a file exporter.
func (w *world) fileLine(i int) (map[string]any, error) {
	lines, err := w.fileLines(i)
	if err != nil {
		return nil, err
	}
	if len(lines) != 1 {
		return nil, failf("calls.jsonl has %d lines, want one", len(lines))
	}
	return lines[0], nil
}

// lineAttrs are the attributes of the first file exporter's only line.
func (w *world) lineAttrs() (map[string]any, error) {
	line, err := w.fileLine(0)
	if err != nil {
		return nil, err
	}
	attrs, _ := line["attributes"].(map[string]any)
	return attrs, nil
}

// lineBody checks that an attribute of the line is want, base64 encoded.
func (w *world) lineBody(key, want string) error {
	attrs, err := w.lineAttrs()
	if err != nil {
		return err
	}
	got, err := base64.StdEncoding.DecodeString(sprint(attrs[key]))
	if err != nil {
		return failf("%s is not base64: %v", key, attrs[key])
	}
	if string(got) != want {
		return failf("%s holds %q, want %q", key, got, want)
	}
	return nil
}

// waitForLines waits until calls.jsonl has n lines, while the proxy runs.
func (w *world) waitForLines(n int) error {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		data, _ := os.ReadFile(filepath.Join(w.files[0].path, "calls.jsonl"))
		if bytes.Count(data, []byte("\n")) >= n {
			return nil
		}
	}
	return failf("%d lines were not written", n)
}

// parseLines reads lines of JSON, each a whole object ending in a line
// break, with numbers kept as written.
func parseLines(data []byte) ([]map[string]any, error) {
	var lines []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, 16<<20)
	for scanner.Scan() {
		d := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		d.UseNumber()
		var line map[string]any
		if err := d.Decode(&line); err != nil {
			return nil, failf("a line is not JSON: %v", err)
		}
		lines = append(lines, line)
	}
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		return nil, failf("the last line is cut")
	}
	return lines, scanner.Err()
}
