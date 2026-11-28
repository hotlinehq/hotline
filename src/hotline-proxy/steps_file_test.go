package proxy_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	// raw, when set, is the whole entry in place of the generated one.
	raw string
}

func fileSteps(sc *godog.ScenarioContext, sut *fileExportSUT) {
	sc.Step(`^two OTLP exporters and a file exporter$`, func() error {
		sut.otlp.addExporter()
		sut.exporters = append(sut.exporters, fileExporter{path: filepath.Join(sut.proxy.dir, "calls")})
		return nil
	})
	sc.Step(`^both exporters get deployment.environment.name staging$`, func() error {
		sut.proxy.settle()
		for i, r := range sut.otlp.all() {
			records := r.got()
			if len(records) != 1 {
				return failf("exporter %d got %d records", i+1, len(records))
			}
			if err := has(records[0].resource, "deployment.environment.name", "staging"); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the file line has the same resource$`, func() error {
		lines, err := sut.folderLines()
		if err != nil {
			return err
		}
		for _, line := range lines {
			if line["event"] == "hotline.request" {
				if got, want := fmt.Sprint(lineAsReceived(line).resource), fmt.Sprint(sut.otlp.records()[0].resource); got != want {
					return failf("the file line's resource is %s, the exporter's %s", got, want)
				}
				return nil
			}
		}
		return failf("the file has no call line")
	})
	var secondErr error
	sc.Step(`^a file exporter$`, func() error {
		sut.exporters = append(sut.exporters, fileExporter{path: filepath.Join(sut.proxy.dir, "calls")})
		return nil
	})
	sc.Step(`^no file exporter$`, func() error {
		sut.exporters = nil
		return nil
	})
	sc.Step(`^no OTLP exporter$`, func() error {
		sut.otlp.removeExporters()
		return nil
	})
	sc.Step(`^the file exporter has maxFileSize (\S+) and maxFiles (\d+)$`, func(size, count string) error {
		sut.exporters[0].extra = "      maxFileSize: " + size + "\n      maxFiles: " + count + "\n"
		return nil
	})
	sc.Step(`^the file exporter has maxFileSize (\S+), maxFiles (\d+) and compression none$`, func(size, count string) error {
		sut.exporters[0].extra = "      maxFileSize: " + size + "\n      maxFiles: " + count + "\n      compression: none\n"
		return nil
	})
	sc.Step(`^the file exporter has queueMemory (\S+)$`, func(size string) error {
		sut.exporters[0].extra = "      queueMemory: " + size + "\n"
		return nil
	})
	sc.Step(`^the file exporter's path is a file, not a folder$`, func() error {
		sut.exporters[0].path = sut.proxy.writeFile("calls", "not a folder\n")
		return nil
	})
	sc.Step(`^a second file exporter on the same folder$`, func() error {
		sut.exporters = append(sut.exporters, sut.exporters[0])
		return nil
	})
	sc.Step(`^a second file exporter in its own folder$`, func() error {
		sut.exporters = append(sut.exporters, fileExporter{path: filepath.Join(sut.proxy.dir, "calls-2")})
		return nil
	})
	sc.Step(`^the integration has (content|maxBodySize) (\S+)$`, func(name, value string) error {
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.talonAPI(""), sut.apis.stripeAPI("")) + "    " + name + ": " + value + "\n")
		return nil
	})
	sc.Step(`^the integration has content full with maxBodySize (\S+)$`, func(size string) error {
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.talonAPI(""), sut.apis.stripeAPI("")) + "    content: full\n    maxBodySize: " + size + "\n")
		return nil
	})
	sc.Step(`^the integration has content full and a spec whose (.+)$`, func(which string) error {
		spec, ok := secretSpecs()[which]
		if !ok {
			return failf("no spec whose %s", which)
		}
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.specAPI("api.x.com", spec, "")) + "    content: full\n")
		sut.app.method, sut.app.target = "GET", "https://api.x.com/v1/things"
		return nil
	})
	sc.Step(`^a call to a host without an api carries "([^"]+)" and "([^"]+)"$`, func(first, second string) error {
		sut.app.target = "https://api.other.test/v1/things"
		return sut.app.defaultCall(first, second)
	})
	sc.Step(`^the line's hotline.content.url.full is "([^"]+)"$`, func(full string) error {
		attrs, err := sut.lineAttrs()
		if err != nil {
			return err
		}
		return has(attrs, "hotline.content.url.full", full)
	})
	sc.Step(`^the vendor responds with a 5KiB body$`, func() error {
		sut.vendors.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			rw.Header().Set("Content-Type", "text/plain")
			_, _ = rw.Write(longResponse())
		})
		return nil
	})
	sc.Step(`^10 calls end$`, func() error {
		for range 10 {
			if err := sut.app.defaultCall("Content-Type: application/json"); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^a call with a 4KiB request body ends$`, func() error {
		return sut.app.callWithBody(strings.Repeat("x", 4096), "Content-Type: text/plain")
	})
	sc.Step(`^a call with a JSON body and an Authorization header ends$`, func() error {
		return sut.app.callWithBody(`{"name":"Ana \"A\"","items":[1,2]}`, "Content-Type: application/json", "Authorization: Bearer secret-token-1")
	})
	sc.Step(`^a call with a Proxy-Trace header ends$`, func() error {
		return sut.app.defaultCall("Content-Type: application/json", "Proxy-Trace: trace-7")
	})
	sc.Step(`^a call to a host without an api ends$`, func() error {
		sut.app.target = "https://api.other.test/v1/things?q=1"
		return sut.app.callWithBody(`{"thing":1}`, "Content-Type: application/json")
	})
	sc.Step(`^the proxy runs and a call ended$`, func() error {
		if err := sut.app.defaultCall("Content-Type: application/json"); err != nil {
			return err
		}
		return sut.waitForLines(1)
	})
	sc.Step(`^the folder is deleted and another call ends$`, func() error {
		if err := os.RemoveAll(sut.exporters[0].path); err != nil {
			return err
		}
		return sut.app.defaultCall("Content-Type: application/json")
	})
	sc.Step(`^a second proxy starts with a file exporter on the same folder$`, func() error {
		// Its first file exporter has a folder of its own, so the proxy
		// must let go of that one when the second folder is taken.
		config := strings.Replace(sut.proxy.configText(), sut.proxy.addr, sut.proxy.ports.take(), 1)
		config = strings.Replace(config, "exporters:\n", "exporters:\n  - file:\n      path: "+filepath.Join(sut.proxy.dir, "calls-other")+"\n", 1)
		second, err := proxy.Start(context.Background(), proxy.Options{
			ConfigPath:    sut.proxy.writeFile("second.yaml", config),
			Resolver:      sut.vendors.network,
			Dialer:        sut.vendors.network,
			Packets:       sut.vendors.network,
			RootCAs:       sut.vendors.authority.pool,
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

	sc.Step(`^the file exporter is set with (.+)$`, func(setting string) error {
		switch setting {
		case "no path":
			sut.exporters[0].path = ""
		case "an otlp endpoint as well":
			sut.exporters[0].extra = "    otlp:\n      endpoint: http://127.0.0.1:4318\n"
		case "nothing in it":
			sut.exporters[0].raw = "  - {}\n"
		default:
			name, value, _ := strings.Cut(setting, " ")
			sut.exporters[0].extra = "      " + name + ": " + value + "\n"
		}
		return nil
	})
	sc.Step(`^the folder is replaced by a file and another call ends$`, func() error {
		if err := os.RemoveAll(sut.exporters[0].path); err != nil {
			return err
		}
		if err := os.WriteFile(sut.exporters[0].path, []byte("not a folder\n"), 0o600); err != nil {
			return err
		}
		return sut.app.defaultCall("Content-Type: application/json")
	})
	sc.Step(`^a call with a JSON body to a vendor that cannot be reached ends$`, func() error {
		sut.vendors.missing["api.talon.one"] = true
		return sut.app.callWithBody(`{"name":"Ana"}`, "Content-Type: application/json")
	})
	sc.Step(`^the proxy warns that the export failed$`, func() error {
		sut.proxy.settle()
		if !strings.Contains(sut.proxy.logs.String(), "export failed") {
			return failf("the proxy did not warn: %s", sut.proxy.logs.String())
		}
		return nil
	})
	sc.Step(`^calls.jsonl has one line$`, func() error {
		_, err := sut.fileLine(0)
		return err
	})
	sc.Step(`^calls.jsonl has no line$`, func() error {
		lines, err := sut.fileLines(0)
		if errors.Is(err, fs.ErrNotExist) {
			// Nothing was ever written, so there is no file yet.
			return nil
		}
		if err != nil {
			return err
		}
		if len(lines) > 0 {
			return failf("calls.jsonl has %d lines", len(lines))
		}
		return nil
	})
	sc.Step(`^the line has the time, a severity, event hotline.request, the trace and span ID, and the resource service.name hotline-proxy$`, func() error {
		line, err := sut.fileLine(0)
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
		attrs, err := sut.lineAttrs()
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
		attrs, err := sut.lineAttrs()
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
	sc.Step(`^each span in the line has the status the OTLP receiver got, and one is ok$`, func() error {
		attrs, err := sut.lineAttrs()
		if err != nil {
			return err
		}
		t, err := sut.otlp.lastTrace()
		if err != nil {
			return err
		}
		statuses := map[string]string{}
		for _, s := range t.spans {
			statuses[s.spanID] = s.status
		}
		spans, _ := attrs["hotline.trace.spans"].([]any)
		seen := map[string]bool{}
		for _, s := range spans {
			span, _ := s.(map[string]any)
			id, status := sprint(span["span_id"]), sprint(span["status"])
			if statuses[id] != status {
				return failf("%v has the status %s in the line, %q in the trace", span["name"], status, statuses[id])
			}
			seen[status] = true
		}
		if !seen["ok"] {
			return failf("no span is ok: %v", spans)
		}
		return nil
	})
	sc.Step(`^the line's hotline.content.request.body holds the first 1KiB of the body sent$`, func() error {
		return sut.lineBody("hotline.content.request.body", sut.app.sentBody[:1024])
	})
	sc.Step(`^the folder holds no other file than calls.jsonl$`, func() error {
		sut.proxy.settle()
		entries, err := os.ReadDir(sut.exporters[0].path)
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
		record, err := sut.otlp.record()
		if err != nil {
			return err
		}
		attrs, err := sut.lineAttrs()
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
			if data, err := os.ReadFile(filepath.Join(sut.exporters[0].path, "calls.jsonl")); err == nil && bytes.Count(data, []byte("\n")) == 1 {
				return nil
			}
			pause(10 * time.Millisecond)
		}
		return failf("no line was written within a second")
	})
	sc.Step(`^calls.jsonl is plain text$`, func() error {
		sut.proxy.settle()
		data, err := os.ReadFile(filepath.Join(sut.exporters[0].path, "calls.jsonl"))
		if err != nil {
			return err
		}
		_, err = parseLines(data)
		return err
	})
	sc.Step(`^there are (\d+) rotated files, each ending in (\S+) and holding only whole lines$`, func(count int, ending string) error {
		sut.proxy.settle()
		rotated, _ := filepath.Glob(filepath.Join(sut.exporters[0].path, "calls-*"))
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
		return sut.proxy.refusedWith("file exporter", "cannot be written to")
	})
	sc.Step(`^the proxy refuses the config, saying (.+)$`, func(problem string) error {
		return sut.proxy.refusedWith(problem)
	})
	sc.Step(`^both folders hold the call's line$`, func() error {
		for i := range sut.exporters {
			if _, err := sut.fileLine(i); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the second proxy refuses to start, naming the folder$`, func() error {
		if secondErr == nil {
			return failf("the second proxy started")
		}
		if !strings.Contains(secondErr.Error(), sut.exporters[0].path) || !strings.Contains(secondErr.Error(), "in use") {
			return failf("the second proxy said %q", secondErr)
		}
		return nil
	})
	sc.Step(`^the call succeeds$`, func() error {
		if sut.app.resp == nil || sut.app.resp.StatusCode != http.StatusOK {
			return failf("the app got %v (%v)", sut.app.resp, sut.app.respErr)
		}
		return nil
	})
	sc.Step(`^calls.jsonl is back in the folder with the new call's line$`, func() error {
		_, err := sut.fileLine(0)
		return err
	})
	sc.Step(`^the proxy warns that the queue was full$`, func() error {
		sut.proxy.settle()
		if !strings.Contains(sut.proxy.logs.String(), "export queue full") {
			return failf("the proxy did not warn: %s", sut.proxy.logs.String())
		}
		return nil
	})
	sc.Step(`^the line's (\S+) decodes to the exact body sent$`, func(key string) error {
		return sut.lineBody(key, sut.app.sentBody)
	})
	sc.Step(`^the line's (\S+) decodes to the vendor's response$`, func(key string) error {
		return sut.lineBody(key, `{"ok":true}`)
	})
	sc.Step(`^the line's hotline.content.response.body holds the first 1KiB$`, func() error {
		return sut.lineBody("hotline.content.response.body", string(longResponse()[:1024]))
	})
	sc.Step(`^the line has hotline.content.request.header.(\S+) as sent$`, func(name string) error {
		attrs, err := sut.lineAttrs()
		if err != nil {
			return err
		}
		return has(attrs, "hotline.content.request.header."+name, []any{headerValue(sut.app.lastSent, name)})
	})
	sc.Step(`^the line has ([a-z][\w-]*\.[\w.-]+) (\S+)$`, func(key, value string) error {
		attrs, err := sut.lineAttrs()
		if err != nil {
			return err
		}
		if list, ok := attrs[key].([]any); ok && len(list) == 1 {
			return has(map[string]any{key: list[0]}, key, value)
		}
		return has(attrs, key, value)
	})
	sc.Step(`^the line has hotline.content.response.body.truncated true and http.response.body.size 5120$`, func() error {
		attrs, err := sut.lineAttrs()
		if err != nil {
			return err
		}
		if err := has(attrs, "hotline.content.response.body.truncated", true); err != nil {
			return err
		}
		return has(attrs, "http.response.body.size", 5120)
	})
	sc.Step(`^the line has no (\S+)$`, func(key string) error {
		attrs, err := sut.lineAttrs()
		if err != nil {
			return err
		}
		return hasNo(attrs, key)
	})
	sc.Step(`^the line's hotline.content.url.full is the address as sent$`, func() error {
		attrs, err := sut.lineAttrs()
		if err != nil {
			return err
		}
		return has(attrs, "hotline.content.url.full", sut.app.target)
	})
	sc.Step(`^the app gets all 5KiB$`, func() error {
		if len(sut.app.respBody) != 5120 {
			return failf("the app got %d bytes (%v)", len(sut.app.respBody), sut.app.respErr)
		}
		return nil
	})
	sc.Step(`^the OTLP record has no hotline.content.request.body and no hotline.content.request.header.authorization$`, func() error {
		record, err := sut.otlp.record()
		if err != nil {
			return err
		}
		return hasNo(record, "hotline.content.request.body", "hotline.content.request.header.authorization", "http.request.header.authorization")
	})
	sc.Step(`^the line's hotline.request.body.hash is the body hash of the saved body, with the line's trace id and span id$`, func() error {
		line, err := sut.fileLine(0)
		if err != nil {
			return err
		}
		attrs, _ := line["attributes"].(map[string]any)
		saved, err := base64.StdEncoding.DecodeString(sprint(attrs["hotline.content.request.body"]))
		if err != nil {
			return err
		}
		return has(attrs, "hotline.request.body.hash", bodyHash("request", sprint(line["trace_id"]), sprint(line["span_id"]), saved))
	})
	sc.Step(`^the line has the request and response headers and the response body$`, func() error {
		attrs, err := sut.lineAttrs()
		if err != nil {
			return err
		}
		for key, want := range map[string]any{
			"hotline.content.request.header.content-type":  []any{"application/json"},
			"hotline.content.response.header.x-request-id": []any{"req-5521"},
		} {
			if err := has(attrs, key, want); err != nil {
				return err
			}
		}
		return sut.lineBody("hotline.content.response.body", `{"ok":true}`)
	})
	sc.Step(`^the proxy warns that integration talon-shop-a has content full but there is no file exporter$`, func() error {
		if sut.proxy.startErr != nil {
			return sut.proxy.startErr
		}
		if !strings.Contains(sut.proxy.logs.String(), "integration talon-shop-a has content: full but there is no file exporter") {
			return failf("the proxy did not warn: %s", sut.proxy.logs.String())
		}
		return nil
	})
}

// callWithBody makes the world's call with a body.
func (sut *appSUT) callWithBody(body string, headers ...string) error {
	sut.sentBody = body
	if sut.appID != "" {
		headers = append([]string{basic(sut.appID + ":x")}, headers...)
	}
	return sut.send(request{method: sut.method, target: sut.target, headers: headers, body: strings.NewReader(body), length: len(body)})
}

// longResponse is a response of 5 KiB that is not the same all along, so a cut
// in the wrong place shows.
func longResponse() []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < 5120; i++ {
		b.WriteString(strings.Repeat(string(rune('a'+i%26)), 64))
	}
	return b.Bytes()[:5120]
}

// fileLines are the lines of a file exporter's calls.jsonl, once the proxy
// has written everything.
func (sut *fileExportSUT) fileLines(i int) ([]map[string]any, error) {
	all, err := sut.allFileLines(i)
	if err != nil {
		return nil, err
	}
	// The ledger's batch lines and the WebSocket session lines are kept
	// apart, so the scenarios about calls see only the calls' lines.
	var calls []map[string]any
	for _, line := range all {
		if line["event"] == "hotline.request" {
			calls = append(calls, line)
		}
	}
	return calls, nil
}

// allFileLines are every line of a file exporter's calls.jsonl, the
// ledger's batch lines among them, once the proxy has stopped.
func (sut *fileExportSUT) allFileLines(i int) ([]map[string]any, error) {
	sut.proxy.settle()
	data, err := os.ReadFile(filepath.Join(sut.exporters[i].path, "calls.jsonl"))
	if err != nil {
		return nil, err
	}
	return parseLines(data)
}

// fileLine is the only line of a file exporter.
func (sut *fileExportSUT) fileLine(i int) (map[string]any, error) {
	lines, err := sut.fileLines(i)
	if err != nil {
		return nil, err
	}
	if len(lines) != 1 {
		return nil, failf("calls.jsonl has %d lines, want one", len(lines))
	}
	return lines[0], nil
}

// lineAttrs are the attributes of the first file exporter's only line.
func (sut *fileExportSUT) lineAttrs() (map[string]any, error) {
	line, err := sut.fileLine(0)
	if err != nil {
		return nil, err
	}
	attrs, _ := line["attributes"].(map[string]any)
	return attrs, nil
}

// lineBody checks that an attribute of the line is want, base64 encoded.
func (sut *fileExportSUT) lineBody(key, want string) error {
	attrs, err := sut.lineAttrs()
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
func (sut *fileExportSUT) waitForLines(n int) error {
	written := eventually(waitLimit, func() bool {
		data, _ := os.ReadFile(filepath.Join(sut.exporters[0].path, "calls.jsonl"))
		return bytes.Count(data, []byte("\n")) >= n
	})
	if !written {
		return failf("%d lines were not written", n)
	}
	return nil
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

// secretSpecs are specs for api.x.com that call one value secret, each in
// its own way.
func secretSpecs() map[string]string {
	return map[string]string{ //nolint:gosec // specs that name where a secret goes, holding none
		"security section names X-Api-Key": `openapi: 3.2.0
paths:
  /v1/things:
    get: {}
components:
  securitySchemes:
    api_key: { type: apiKey, in: header, name: X-Api-Key }
`,
		"header X-Debug-Token is marked x-hotline-redact true": `openapi: 3.2.0
paths:
  /v1/things:
    get:
      parameters:
        - { name: X-Debug-Token, in: header, x-hotline-redact: true }
`,
		"response header Request-Id is marked x-hotline-redact true": `openapi: 3.2.0
paths:
  /v1/things:
    get:
      responses:
        "200":
          description: ok
          headers:
            Request-Id: { schema: { type: string }, x-hotline-redact: true }
`,
		"path value id is marked x-hotline-redact true": `openapi: 3.2.0
paths:
  /v1/things/{id}:
    get:
      parameters:
        - { name: id, in: path, required: true, schema: { type: string }, x-hotline-redact: true }
`,
		"security section names the query word key": `openapi: 3.2.0
paths:
  /v1/things:
    get: {}
components:
  securitySchemes:
    query_key: { type: apiKey, in: query, name: key }
`,
	}
}

// fileExportSUT is the file exporters: the folders they write their lines to.
type fileExportSUT struct {
	proxy   *proxySUT
	vendors *vendorsSUT
	app     *appSUT
	apis    *apisSUT
	otlp    *otlpSUT
	// exporters are the file exporters: each one's folder and more lines.
	exporters []fileExporter
}

func newFileExportSUT(root *proxySUT, vendors *vendorsSUT, app *appSUT, apis *apisSUT, otlp *otlpSUT) *fileExportSUT {
	sut := &fileExportSUT{proxy: root, vendors: vendors, app: app, apis: apis, otlp: otlp}
	root.addConfig(configExporters, sut.section)
	return sut
}

// AddSteps adds the steps of the file exporters.
func (sut *fileExportSUT) AddSteps(sc *godog.ScenarioContext) {
	fileSteps(sc, sut)
}

// section is the config's file exporters.
func (sut *fileExportSUT) section() string {
	var b strings.Builder
	for _, f := range sut.exporters {
		if f.raw != "" {
			b.WriteString(f.raw)
			continue
		}
		fmt.Fprintf(&b, "  - file:\n      path: %s\n%s", f.path, f.extra)
	}
	return b.String()
}

// add adds a file exporter writing to folder, a folder of the scenario's,
// with extra as more lines of its config.
func (sut *fileExportSUT) add(folder, extra string) {
	sut.exporters = append(sut.exporters, fileExporter{path: filepath.Join(sut.proxy.dir, folder), extra: extra})
}

// count is how many file exporters the config has.
func (sut *fileExportSUT) count() int { return len(sut.exporters) }

// folder is where the file exporter at place i writes.
func (sut *fileExportSUT) folder(i int) string { return sut.exporters[i].path }

// setExporterLines gives the file exporter at place i extra as the more lines of
// its config, in place of the ones it had.
func (sut *fileExportSUT) setExporterLines(i int, extra string) { sut.exporters[i].extra = extra }

// moveFirst makes the first file exporter write to another folder of the
// scenario's.
func (sut *fileExportSUT) moveFirst(folder string) {
	sut.exporters[0].path = filepath.Join(sut.proxy.dir, folder)
}

// removeAll leaves every file exporter out of the config.
func (sut *fileExportSUT) removeAll() { sut.exporters = nil }
