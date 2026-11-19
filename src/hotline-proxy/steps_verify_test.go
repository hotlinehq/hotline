package proxy_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cucumber/godog"

	proxy "hotline-proxy"
)

// verified is what one run of hotline-proxy verify gave.
type verified struct {
	out  string
	code int
}

// exporterLedgerSteps are the steps about what each exporter does with the
// ledger: the records it dropped, and batch records that wait.
func exporterLedgerSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^an OTLP exporter and a file exporter$`, func() error {
		w.files = append(w.files, fileExporter{path: filepath.Join(w.dir, "calls")})
		return nil
	})
	sc.Step(`^the file exporter's queue is full for one record$`, func() error {
		w.fileExporterOnce()
		w.fullForThird()
		return nil
	})
	sc.Step(`^the file exporter's queue is full(?: for 1,025 records)?$`, func() error {
		w.fileExporterOnce()
		w.files[0].extra = "      queueMemory: 1\n"
		return nil
	})
	sc.Step(`^the queue has room again$`, func() error { return nil })
	sc.Step(`^the file got 2 records and a batch record whose hotline.batch.dropped lists the index and hash of the third$`, func() error {
		records, batches, err := w.fileLedger()
		if err != nil {
			return err
		}
		if len(records) != 2 || len(batches) == 0 {
			return failf("the file got %d records and %d batch records", len(records), len(batches))
		}
		otlp, _ := w.receivers[0].got()
		third := otlp[len(otlp)-1].attrs
		want := []any{map[string]any{"index": json.Number("2"), "hash": third["hotline.record.hash"]}}
		if got := batches[0]["hotline.batch.dropped"]; fmt.Sprint(got) != fmt.Sprint(want) {
			return failf("hotline.batch.dropped is %v, want %v", got, want)
		}
		return nil
	})
	sc.Step(`^the root rebuilds from the 2 records and the dropped hash$`, func() error {
		records, batches, err := w.fileLedger()
		if err != nil {
			return err
		}
		leaves := make([]string, 3)
		for _, r := range records {
			index, _ := r["hotline.batch.index"].(json.Number).Int64()
			leaves[index] = sprint(r["hotline.record.hash"])
		}
		dropped := batches[0]["hotline.batch.dropped"].([]any)[0].(map[string]any)
		leaves[2] = sprint(dropped["hash"])
		if root := rootOf(leaves); root != batches[0]["hotline.batch.root"] {
			return failf("the root rebuilds as %s, the batch has %v", root, batches[0]["hotline.batch.root"])
		}
		return nil
	})
	sc.Step(`^the OTLP exporter's batch record has no hotline.batch.dropped$`, func() error {
		batches := w.receivers[0].ledger()
		if len(batches) == 0 {
			return failf("the OTLP exporter got no batch record")
		}
		return hasNo(batches[0].attrs, "hotline.batch.dropped", "hotline.batch.dropped_count")
	})
	sc.Step(`^both batch records have the same hotline.batch.signature$`, func() error {
		_, batches, err := w.fileLedger()
		if err != nil {
			return err
		}
		return has(w.receivers[0].ledger()[0].attrs, "hotline.batch.signature", batches[0]["hotline.batch.signature"])
	})
	sc.Step(`^the file's batch record has hotline.batch.dropped_count 1025 and no hotline.batch.dropped$`, func() error {
		_, batches, err := w.fileLedger()
		if err != nil {
			return err
		}
		if fmt.Sprint(batches[0]["hotline.batch.dropped_count"]) != "1025" {
			return failf("hotline.batch.dropped_count is %v", batches[0]["hotline.batch.dropped_count"])
		}
		return hasNo(batches[0], "hotline.batch.dropped")
	})
	sc.Step(`^that batch cannot be rebuilt from the file's copy$`, func() error {
		result := w.verify(proxy.VerifyOptions{Folder: w.files[0].path})
		if result.code != 1 || !strings.Contains(result.out, "cannot be proven from this copy") {
			return failf("verify gave %d: %s", result.code, result.out)
		}
		return nil
	})
	sc.Step(`^the file got the batch record$`, func() error {
		_, batches, err := w.fileLedger()
		if err != nil {
			return err
		}
		if len(batches) == 0 {
			return failf("the file got no batch record")
		}
		return nil
	})
	sc.Step(`^the OTLP exporter's collector is down$`, func() error {
		w.receivers[0].setDown(true)
		return nil
	})
	sc.Step(`^the collector is back$`, func() error {
		// Sends refused a moment ago end at once; let them.
		time.Sleep(300 * time.Millisecond)
		w.receivers[0].setDown(false)
		return nil
	})
	sc.Step(`^the collector got the newest 64 waiting batch records$`, func() error {
		if _, err := w.waitBatches(64); err != nil {
			return err
		}
		time.Sleep(200 * time.Millisecond)
		batches := w.receivers[0].ledger()
		if len(batches) != 64 {
			return failf("the collector got %d batch records", len(batches))
		}
		for i, b := range batches {
			if err := has(b.attrs, "hotline.batch.sequence", int64(i+2)); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the first one it got has hotline.batch.lost_batches 2$`, func() error {
		return has(w.receivers[0].ledger()[0].attrs, "hotline.batch.lost_batches", int64(2))
	})
	sc.Step(`^the proxy's memory stayed bounded$`, func() error {
		// At most 64 batch records waited: the collector got no more than
		// that from the time it was down.
		if n := len(w.receivers[0].ledger()); n > 64 {
			return failf("%d batch records waited", n)
		}
		return nil
	})
	verifySteps(sc, w)
}

// fullForThird fills the file exporter's queue for the third call only: the
// queue holds 10KiB, and the vendor's third response has a header of 20KiB,
// which the record keeps, so that record alone does not fit.
// fileExporterOnce adds a file exporter when the scenario has none yet.
func (w *world) fileExporterOnce() {
	if len(w.files) == 0 {
		w.files = append(w.files, fileExporter{path: filepath.Join(w.dir, "calls")})
	}
}

func (w *world) fullForThird() {
	w.files[0].extra = "      queueMemory: 10KiB\n"
	var n atomic.Int64
	w.vendor("api.shop.test").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		id := "r1"
		if n.Add(1) == 3 {
			id = strings.Repeat("r", 20<<10)
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.Header().Set("X-Request-Id", id)
		_, _ = rw.Write([]byte(`{"orders":[]}`))
	})
}

// fileLedger are the attributes of the file exporter's call lines and of
// its batch lines, once the proxy has stopped.
func (w *world) fileLedger() ([]map[string]any, []map[string]any, error) {
	w.settle()
	data, err := os.ReadFile(filepath.Join(w.files[0].path, "calls.jsonl"))
	if err != nil {
		return nil, nil, err
	}
	var records, batches []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.UseNumber()
		var parsed map[string]any
		if err := decoder.Decode(&parsed); err != nil {
			return nil, nil, err
		}
		attrs, _ := parsed["attributes"].(map[string]any)
		if parsed["event"] == "hotline.ledger.batch" {
			batches = append(batches, attrs)
		} else {
			records = append(records, attrs)
		}
	}
	return records, batches, nil
}

// verify runs hotline-proxy verify.
func (w *world) verify(o proxy.VerifyOptions) verified {
	var out bytes.Buffer
	code := proxy.Verify(&out, o)
	return verified{out: out.String(), code: code}
}

// verifySteps are the steps of verify.feature.
func verifySteps(sc *godog.ScenarioContext, w *world) {
	var result verified
	// changedSpan is the span id of the record a step changed.
	var changedSpan string
	sc.Step(`^a file exporter writing to the folder calls$`, func() error {
		w.files = append(w.files, fileExporter{path: filepath.Join(w.dir, "calls")})
		if len(w.integrations) == 0 {
			// The folder's calls are checkout's, with the shop spec.
			line, err := w.shopAPI("", "")
			if err != nil {
				return err
			}
			w.addIntegration(integration("checkout", line))
			w.appID = "checkout"
		}
		return nil
	})
	sc.Step(`^the file exporter starts a new file past 1 KiB and compresses the old ones$`, func() error {
		w.files[0].extra = "      maxFileSize: 1KiB\n      maxFiles: 200\n"
		return nil
	})
	sc.Step(`^the folder calls from a stopped proxy(?:, with (\d+) calls)?(, with calls in 3 batch windows)?$`, func(calls, windows string) error {
		switch {
		case windows != "":
			for range 3 {
				if err := w.ordersTimes(1); err != nil {
					return err
				}
				w.clock.pass(10 * time.Second)
			}
		default:
			n := 1
			_, _ = fmt.Sscan(calls, &n)
			if err := w.ordersTimes(n); err != nil {
				return err
			}
		}
		w.settle()
		return nil
	})
	sc.Step(`^the folder calls from a proxy that was killed$`, func() error {
		// What a killed proxy leaves is its folder as it was while it ran:
		// the folder is copied then, and the proxy writes on elsewhere.
		w.files[0].path = filepath.Join(w.dir, "running")
		if err := w.ordersTimes(2); err != nil {
			return err
		}
		w.clock.pass(10 * time.Second)
		if err := w.ordersTimes(1); err != nil {
			return err
		}
		running := filepath.Join(w.dir, "running", "calls.jsonl")
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			data, _ := os.ReadFile(running) //nolint:gosec // the scenario's own folder
			if bytes.Count(data, []byte("\n")) >= 4 {
				_ = os.MkdirAll(filepath.Join(w.dir, "calls"), 0o750)
				return os.WriteFile(filepath.Join(w.dir, "calls", "calls.jsonl"), data, 0o600) //nolint:gosec // the scenario's own folder
			}
			if time.Now().After(deadline) {
				return failf("the running proxy wrote %d lines", bytes.Count(data, []byte("\n")))
			}
		}
	})
	sc.Step(`^I run hotline-proxy verify (.+)$`, func(command string) error {
		// The proxy needs no config and no network to verify a folder.
		o, err := w.verifyOptions(strings.TrimSuffix(command, " with no config and no network"))
		if err != nil {
			return err
		}
		if len(w.files) == 0 || !strings.Contains(w.files[0].path, "running") {
			w.settle()
		}
		result = w.verify(o)
		return nil
	})
	sc.Step(`^the file writes shop.order.weight as 2.0 and as 1.5$`, func() error {
		data, err := os.ReadFile(filepath.Join(w.files[0].path, "calls.jsonl"))
		if err != nil {
			return err
		}
		for _, want := range []string{`"shop.order.weight":2.0`, `"shop.order.weight":1.5`} {
			if !bytes.Contains(data, []byte(want)) {
				return failf("the file has no %s: %s", want, data)
			}
		}
		return nil
	})
	sc.Step(`^it exits with (\d)$`, func(code int) error {
		if result.code != code {
			return failf("verify exited with %d, want %d: %s", result.code, code, result.out)
		}
		return nil
	})
	sc.Step(`^it prints that checkout's 5 records and their traces in 2 batches check out, and the run ended with a clean stop$`, func() error {
		return result.prints("checkout's 5 records and their traces in 2 batches check out", "the run ended with a clean stop")
	})
	sc.Step(`^it read every file in the folder, in order$`, func() error {
		entries, _ := os.ReadDir(w.files[0].path)
		gz := 0
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".gz") {
				gz++
			}
		}
		if gz < 2 {
			return failf("the folder has %d compressed files", gz)
		}
		return result.prints("checkout's 50 records", "everything checks out")
	})
	sc.Step(`^it prints 2 runs, each with its own instance, from sequence 0, ending with a clean stop$`, func() error {
		lines := result.lines("instance ")
		if len(lines) != 2 || !strings.Contains(lines[0], "proxy-test-1") || !strings.Contains(lines[1], "proxy-test-2") {
			return failf("verify printed %s", result.out)
		}
		for _, line := range lines {
			if !strings.Contains(line, "sequence 0 to") || !strings.Contains(line, "clean stop") {
				return failf("verify printed %s", line)
			}
		}
		return nil
	})
	sc.Step(`^the app sends GET /orders as checkout (\d+) times and GET /refunds as refunds (\d+) times$`, func(a, b int) error {
		w.appID = "checkout"
		if err := w.ordersTimes(a); err != nil {
			return err
		}
		w.appID = "refunds"
		for range b {
			if err := w.shopCall("GET", "/refunds", true, "", "", ""); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^only the checkout records and batch records are kept in the folder$`, func() error {
		return w.keepLines(func(line map[string]any) bool {
			attrs, _ := line["attributes"].(map[string]any)
			return attrs["hotline.integration.id"] == "checkout" || attrs["hotline.batch.integration"] == "checkout"
		})
	})
	sc.Step(`^it prints that checkout's 3 records check out, and mentions no other integration$`, func() error {
		if strings.Contains(result.out, "refunds") {
			return failf("verify mentions refunds: %s", result.out)
		}
		return result.prints("checkout's 3 records")
	})
	sc.Step(`^it prints one line for checkout and one for refunds, each with its own batches$`, func() error {
		if len(result.lines("checkout's ")) != 1 || len(result.lines("refunds's ")) != 1 {
			return failf("verify printed %s", result.out)
		}
		return nil
	})
	sc.Step(`^(http.response.status_code is changed|http.response.status_code is changed and hotline.record.hash recomputed) in the second record$`, func(change string) error {
		return w.changeLine(2, func(line map[string]any) {
			attrs := line["attributes"].(map[string]any)
			attrs["http.response.status_code"] = json.Number("599")
			changedSpan = sprint(line["span_id"])
			if strings.Contains(change, "recomputed") {
				attrs["hotline.record.hash"] = lineRecordHash(line)
			}
		})
	})
	sc.Step(`^the second record is deleted$`, func() error {
		return w.changeLine(2, nil)
	})
	sc.Step(`^a copy of the second record with a new span id and recomputed hash is added$`, func() error {
		lines, err := w.folderLines()
		if err != nil {
			return err
		}
		calls := 0
		for _, line := range lines {
			if line["event"] == "hotline.request" {
				calls++
				if calls == 2 {
					copied := cloneLine(line)
					copied["span_id"] = "1111111111111111"
					copied["attributes"].(map[string]any)["hotline.record.hash"] = lineRecordHash(copied)
					lines = append(lines, copied)
					break
				}
			}
		}
		return w.writeLines(lines)
	})
	sc.Step(`^one span is removed from the second line's spans$`, func() error {
		return w.changeLine(2, func(line map[string]any) {
			attrs := line["attributes"].(map[string]any)
			spans := attrs["hotline.trace.spans"].([]any)
			changedSpan = sprint(line["span_id"])
			attrs["hotline.trace.spans"] = spans[:len(spans)-1]
		})
	})
	sc.Step(`^the end of a span is changed in the second line's spans$`, func() error {
		return w.changeLine(2, func(line map[string]any) {
			spans := line["attributes"].(map[string]any)["hotline.trace.spans"].([]any)
			changedSpan = sprint(line["span_id"])
			spans[len(spans)-1].(map[string]any)["end_time"] = "2001-01-01T00:00:00.000000Z"
		})
	})
	sc.Step(`^the (span_id|parent_span_id) of the last span in the second line's spans is changed to (the same id in capital letters|zeros, which stand for no span)$`, func(field, id string) error {
		return w.changeLine(2, func(line map[string]any) {
			spans := line["attributes"].(map[string]any)["hotline.trace.spans"].([]any)
			changedSpan = sprint(line["span_id"])
			last := spans[len(spans)-1].(map[string]any)
			if strings.HasPrefix(id, "zeros") {
				last[field] = "0000000000000000"
			} else {
				// An id of digits alone has no letter to make capital, so
				// its first digit becomes one.
				upper := strings.ToUpper(sprint(last[field]))
				if !strings.ContainsAny(upper, "ABCDEF") {
					upper = "A" + upper[1:]
				}
				last[field] = upper
			}
		})
	})
	sc.Step(`^hotline.batch.records is changed in the first batch record$`, func() error {
		return w.changeBatch(0, func(attrs map[string]any) { attrs["hotline.batch.records"] = json.Number("99") })
	})
	sc.Step(`^the batch record with sequence 1 is deleted$`, func() error {
		return w.keepLines(func(line map[string]any) bool {
			attrs, _ := line["attributes"].(map[string]any)
			return line["event"] != "hotline.ledger.batch" || fmt.Sprint(attrs["hotline.batch.sequence"]) != "1"
		})
	})
	sc.Step(`^batch 2's hotline.batch.previous is changed to the root of batch 0$`, func() error {
		var root any
		if err := w.changeBatch(0, func(attrs map[string]any) { root = attrs["hotline.batch.root"] }); err != nil {
			return err
		}
		return w.changeBatch(2, func(attrs map[string]any) { attrs["hotline.batch.previous"] = root })
	})
	sc.Step(`^it prints that the record with that span id does not match its hotline.record.hash$`, func() error {
		return result.prints("the record with span id " + changedSpan + " does not match its hotline.record.hash")
	})
	sc.Step(`^it prints that batch 0 of that run does not rebuild its root from its records$`, func() error {
		return result.prints("batch 0 of that run does not rebuild its root from its records")
	})
	sc.Step(`^it prints that batch 0 has 2 of 3 records and index 1 is missing$`, func() error {
		return result.prints("batch 0 has 2 of 3 records and index 1 is missing")
	})
	sc.Step(`^it prints that batch 0 holds a record it does not count$`, func() error {
		return result.prints("batch 0 holds a record it does not count")
	})
	sc.Step(`^it prints that the trace of (?:the record with that span id|that record) does not match its hotline.trace.hash$`, func() error {
		return result.prints("the trace of the record with span id " + changedSpan + " does not match its hotline.trace.hash")
	})
	sc.Step(`^it prints that the signature of batch 0 does not check out with its key$`, func() error {
		return result.prints("the signature of batch 0 does not check out with its key")
	})
	sc.Step(`^it prints that the run's chain is broken: batch 1 is missing$`, func() error {
		return result.prints("the run's chain is broken: batch 1 is missing")
	})
	sc.Step(`^it prints that the records naming batch 1 have no batch record$`, func() error {
		return result.prints("have no batch record")
	})
	sc.Step(`^it prints that the chain is broken between batch 1 and batch 2$`, func() error {
		return result.prints("the chain is broken between batch 1 and batch 2")
	})
	sc.Step(`^it prints that the run has no last batch: it crashed, or its end is missing$`, func() error {
		return result.prints("the run has no last batch: it crashed, or its end is missing")
	})
	sc.Step(`^the file exporter's queue was full for one record of batch 0$`, func() error {
		w.fullForThird()
		if err := w.ordersTimes(3); err != nil {
			return err
		}
		w.settle()
		return nil
	})
	sc.Step(`^the file exporter dropped one record of batch 0$`, func() error {
		w.fullForThird()
		if err := w.ordersTimes(3); err != nil {
			return err
		}
		w.settle()
		return nil
	})
	sc.Step(`^the file exporter's queue was full for 1,025 records of batch 0$`, func() error {
		w.files[0].extra = "      queueMemory: 1\n"
		if err := w.ordersTimes(1025); err != nil {
			return err
		}
		w.settle()
		return nil
	})
	sc.Step(`^it prints that batch 0 rebuilds from 2 records and 1 dropped hash$`, func() error {
		return result.prints("batch 0 rebuilds from 2 records and 1 dropped hash")
	})
	sc.Step(`^it prints the index of the dropped record$`, func() error {
		return result.prints("at index 2")
	})
	sc.Step(`^it prints that batch 0 cannot be proven from this copy: 1,025 records were dropped by this exporter$`, func() error {
		return result.prints("batch 0 cannot be proven from this copy: 1,025 records were dropped by this exporter")
	})
	sc.Step(`^a batch record in calls with hotline.batch.lost_batches 2$`, func() error {
		if err := w.ordersTimes(1); err != nil {
			return err
		}
		w.settle()
		return w.changeBatch(0, func(attrs map[string]any) { attrs["hotline.batch.lost_batches"] = json.Number("2") })
	})
	sc.Step(`^it prints that 2 batches before it were lost by this exporter, and their records cannot be proven$`, func() error {
		return result.prints("2 batches before batch 0 were lost by this exporter, and their records cannot be proven")
	})
	sc.Step(`^the vendor answered GET /orders with the json body (.+)$`, func(body string) error {
		w.vendor("api.shop.test").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(body))
		})
		if err := w.shopCall("GET", "/orders", true, "", "", ""); err != nil {
			return err
		}
		w.settle()
		return nil
	})
	sc.Step(`^the body was saved as (\S+)$`, func(name string) error {
		w.writeFile(name, string(w.respBody))
		return nil
	})
	sc.Step(`^it prints that the response body matches the record's hotline.response.body.hash$`, func() error {
		return result.prints("the response body matches the record's hotline.response.body.hash")
	})
	sc.Step(`^it prints that the response body does not match$`, func() error {
		return result.prints("the response body does not match")
	})
	sc.Step(`^the integration has content full and maxBodySize (\S+)$`, func(size string) error {
		line, err := w.shopAPI("", "")
		if err != nil {
			return err
		}
		w.integrations = []string{integration("checkout", line) + "    content: full\n    maxBodySize: " + size + "\n"}
		w.appID = "checkout"
		return nil
	})
	sc.Step(`^the app sends POST /orders with a json body of 3 KiB$`, func() error {
		body := `{"item":"` + strings.Repeat("A", 3<<10) + `","quantity":2}`
		return w.shopCall("POST", "/orders", true, "Idempotency-Key: k1", "json", body)
	})
	sc.Step(`^it prints that 1 kept body matches its hash$`, func() error {
		if result.code != 0 {
			return failf("verify gave %d: %s", result.code, result.out)
		}
		return result.prints("kept bodies match their hash")
	})
	sc.Step(`^it prints that 1 kept body was cut short and could not be checked$`, func() error {
		return result.prints("1 kept bodies were cut short and could not be checked")
	})
	sc.Step(`^standard output is one JSON object with format hotline.verify.v1 and ok (true|false)$`, func(ok string) error {
		report, err := result.json()
		if err != nil {
			return err
		}
		if report["format"] != "hotline.verify.v1" || fmt.Sprint(report["ok"]) != ok {
			return failf("verify printed %s", result.out)
		}
		return nil
	})
	sc.Step(`^its summary counts (\d+) records, (\d+) run and (\d+) problems$`, func(records, runs, problems string) error {
		report, err := result.json()
		if err != nil {
			return err
		}
		summary, _ := report["summary"].(map[string]any)
		if fmt.Sprint(summary["records"]) != records || fmt.Sprint(summary["runs"]) != runs || fmt.Sprint(summary["problems"]) != problems {
			return failf("the summary is %v", summary)
		}
		return nil
	})
	sc.Step(`^(http.response.status_code is changed in a record|a span's end is changed in a record's trace|a record is changed and its hash recomputed|a record is deleted|hotline.batch.records is changed in a batch record|batch 2's hotline.batch.previous is changed|a record's hotline.batch.index is changed to 99|batch 0 is in the file twice|batch 0 names a batch before it)$`, func(change string) error {
		switch change {
		case "a record's hotline.batch.index is changed to 99":
			return w.changeLine(2, func(line map[string]any) {
				line["attributes"].(map[string]any)["hotline.batch.index"] = json.Number("99")
			})
		case "batch 0 is in the file twice":
			return w.twiceBatch0()
		case "batch 0 names a batch before it":
			return w.changeBatch(0, func(attrs map[string]any) { attrs["hotline.batch.previous"] = "sha256:" + strings.Repeat("0", 64) })
		case "http.response.status_code is changed in a record":
			return w.changeLine(2, func(line map[string]any) {
				line["attributes"].(map[string]any)["http.response.status_code"] = json.Number("599")
			})
		case "a span's end is changed in a record's trace":
			return w.changeLine(2, func(line map[string]any) {
				spans := line["attributes"].(map[string]any)["hotline.trace.spans"].([]any)
				spans[len(spans)-1].(map[string]any)["end_time"] = "2001-01-01T00:00:00.000000Z"
			})
		case "a record is changed and its hash recomputed":
			return w.changeLine(2, func(line map[string]any) {
				attrs := line["attributes"].(map[string]any)
				attrs["http.response.status_code"] = json.Number("599")
				attrs["hotline.record.hash"] = lineRecordHash(line)
			})
		case "a record is deleted":
			return w.changeLine(2, nil)
		case "hotline.batch.records is changed in a batch record":
			return w.changeBatch(0, func(attrs map[string]any) { attrs["hotline.batch.records"] = json.Number("99") })
		default:
			var root any
			if err := w.changeBatch(0, func(attrs map[string]any) { root = attrs["hotline.batch.root"] }); err != nil {
				return err
			}
			return w.changeBatch(2, func(attrs map[string]any) { attrs["hotline.batch.previous"] = root })
		}
	})
	sc.Step(`^the kept (url.full|request body) is changed(?: in the record)?$`, func(what string) error {
		return w.changeLine(1, func(line map[string]any) {
			attrs := line["attributes"].(map[string]any)
			if what == "url.full" {
				attrs["hotline.content.url.full"] = "https://changed.example/"
				return
			}
			attrs["hotline.content.request.body"] = base64.StdEncoding.EncodeToString([]byte("{}"))
		})
	})
	sc.Step(`^the checkout integration's shop spec has the overlay (\S+)$`, func(name string) error {
		line, err := w.shopAPI("", "    overlays: [./specs/"+name+"]\n")
		w.integrations = []string{integration("checkout", line)}
		return err
	})
	var namedBatch string
	sc.Step(`^the first batch record loses its hotline.batch.sequence$`, func() error {
		lines, err := w.folderLines()
		if err != nil {
			return err
		}
		for _, line := range lines {
			if line["event"] == "hotline.ledger.batch" {
				attrs := line["attributes"].(map[string]any)
				namedBatch = sprint(attrs["hotline.batch.id"])
				delete(attrs, "hotline.batch.sequence")
				return w.writeLines(lines)
			}
		}
		return failf("the folder has no batch line")
	})
	sc.Step(`^it prints that the signature of that batch, named by its id, does not check out$`, func() error {
		return result.prints("the signature of batch " + namedBatch + " does not check out")
	})
	sc.Step(`^it prints the instance checkout-0 with the run id proxy-test-1$`, func() error {
		return result.prints("instance checkout-0, run proxy-test-1")
	})
	sc.Step(`^it prints that (the body cannot be read|no record has that span id)$`, func(what string) error {
		if what == "the body cannot be read" {
			return result.prints("cannot be read")
		}
		return result.prints("no record has the span id 0000000000000000")
	})
	sc.Step(`^ok is false and the problems hold one with code (\S+), the batch, and the file and line where it was found$`, func(code string) error {
		report, err := result.json()
		if err != nil {
			return err
		}
		if report["ok"] != false {
			return failf("ok is %v", report["ok"])
		}
		problems, _ := report["problems"].([]any)
		for _, p := range problems {
			problem, _ := p.(map[string]any)
			if problem["code"] == code && problem["batch"] != nil && problem["file"] != nil && problem["line"] != nil {
				return nil
			}
		}
		return failf("no problem with code %s, a batch, a file and a line: %s", code, result.out)
	})
	sc.Step(`^a kept body was cut short by maxBodySize$`, func() error {
		line, err := w.shopAPI("", "")
		if err != nil {
			return err
		}
		w.integrations = []string{integration("checkout", line) + "    content: full\n    maxBodySize: 1KiB\n"}
		body := `{"item":"` + strings.Repeat("A", 3<<10) + `","quantity":2}`
		if err := w.shopCall("POST", "/orders", true, "Idempotency-Key: k1", "json", body); err != nil {
			return err
		}
		w.settle()
		return nil
	})
	sc.Step(`^the run or batch has the note (\S+)$`, func(code string) error {
		report, err := result.json()
		if err != nil {
			return err
		}
		notes, _ := report["notes"].([]any)
		for _, n := range notes {
			if note, _ := n.(map[string]any); note["code"] == code {
				return nil
			}
		}
		return failf("no note %s: %s", code, result.out)
	})
	sc.Step(`^a record whose url.full holds a quote, a newline and the character U\+009B$`, func() error {
		if err := w.ordersTimes(1); err != nil {
			return err
		}
		w.settle()
		return nil
	})
	sc.Step(`^that record was changed$`, func() error {
		return w.changeLine(1, func(line map[string]any) {
			line["attributes"].(map[string]any)["url.full"] = "https://api.shop.test/\"\n\u009b"
			line["span_id"] = "\"\n\u009b"
		})
	})
	sc.Step(`^standard output is still one valid JSON object, with those characters escaped$`, func() error {
		if strings.ContainsRune(result.out, '\u009b') || strings.Count(result.out, "\n") != 1 {
			return failf("verify printed %q", result.out)
		}
		_, err := result.json()
		return err
	})
	sc.Step(`^the JSON has a body entry with span_id \S+, side response and matches true$`, func() error {
		report, err := result.json()
		if err != nil {
			return err
		}
		body, _ := report["body"].(map[string]any)
		if body["side"] != "response" || body["matches"] != true || body["span_id"] != w.lastSpanID() {
			return failf("the body entry is %v", body)
		}
		return nil
	})
	sc.Step(`^standard output is one JSON object with ok false and a problem with code input_unreadable$`, func() error {
		report, err := result.json()
		if err != nil {
			return err
		}
		problems, _ := report["problems"].([]any)
		if report["ok"] != false || len(problems) == 0 || problems[0].(map[string]any)["code"] != "input_unreadable" {
			return failf("verify printed %s", result.out)
		}
		return nil
	})
	sc.Step(`^it prints (that the folder does not exist|the file and line it could not read|that hotline.batch.v9 is not a known format|that line 1 has no time|that the compressed file is not gzip|that the compressed file ends too soon|that the rotated file cannot be read)$`, func(what string) error {
		switch what {
		case "that line 1 has no time":
			return result.prints("calls.jsonl line 1 has no time")
		case "that the compressed file is not gzip":
			return result.prints(".jsonl.gz is not gzip")
		case "that the compressed file ends too soon":
			return result.prints(".jsonl.gz is cut off or damaged")
		case "that the rotated file cannot be read":
			return result.prints(".jsonl cannot be read")
		case "that the folder does not exist":
			return result.prints("does not exist")
		case "the file and line it could not read":
			return result.prints("calls.jsonl line 2 is not a line the file exporter writes")
		default:
			return result.prints("hotline.batch.v9 is not a known format")
		}
	})
}

// verifyOptions reads a verify command line from a step: the folder, or
// a description of bad input, then the flags. A --span stands for the span
// id of the call the scenario made, which is new on every run.
func (w *world) verifyOptions(command string) (proxy.VerifyOptions, error) {
	var o proxy.VerifyOptions
	rest := command
	for _, bad := range []string{"a folder that does not exist", "a file that is not JSON lines", "a batch record of format v9",
		"a line with no time", "a compressed file that is not gzip", "a cut-off compressed file", "a rotated file that is a folder"} {
		if after, found := strings.CutPrefix(command, bad); found {
			rest = "calls" + after
			if err := w.badInput(bad); err != nil {
				return o, err
			}
		}
	}
	words := strings.Fields(rest)
	o.Folder = filepath.Join(w.dir, words[0])
	for i := 1; i < len(words); i++ {
		next := ""
		if i+1 < len(words) {
			next = words[i+1]
		}
		switch words[i] {
		case "--format":
			o.JSON, i = next == "json", i+1
		case "--integration":
			o.Integration, i = next, i+1
		case "--body":
			o.Body, i = filepath.Join(w.dir, next), i+1
			if _, err := os.Stat(o.Body); err != nil {
				w.writeFile(next, `{"orders":["another"]}`)
			}
		case "--span":
			// The zero span id stands for one no call has.
			o.Span, i = w.lastSpanID(), i+1
			if next == "0000000000000000" {
				o.Span = next
			}
		case "--side":
			o.Side, i = next, i+1
		}
	}
	return o, nil
}

// badInput makes the folder calls hold input verify cannot read.
func (w *world) badInput(what string) error {
	folder := filepath.Join(w.dir, "calls")
	switch what {
	case "a folder that does not exist":
		w.files = nil
		return os.RemoveAll(folder)
	case "a line with no time":
		_ = os.MkdirAll(folder, 0o750)
		return os.WriteFile(filepath.Join(folder, "calls.jsonl"), []byte(`{"event":"x","attributes":{}}`+"\n"), 0o600)
	case "a compressed file that is not gzip":
		_ = os.MkdirAll(folder, 0o750)
		return os.WriteFile(filepath.Join(folder, "calls-2026-11-16T07-00-00.000.jsonl.gz"), []byte("plain text\n"), 0o600)
	case "a cut-off compressed file":
		var zipped bytes.Buffer
		zw := gzip.NewWriter(&zipped)
		_, _ = zw.Write(bytes.Repeat([]byte(`{"time":"2026-11-16T07:00:00Z","event":"x","attributes":{}}`+"\n"), 100))
		_ = zw.Close()
		_ = os.MkdirAll(folder, 0o750)
		return os.WriteFile(filepath.Join(folder, "calls-2026-11-16T07-00-00.000.jsonl.gz"), zipped.Bytes()[:zipped.Len()/2], 0o600)
	case "a rotated file that is a folder":
		return os.MkdirAll(filepath.Join(folder, "calls-2026-11-16T07-00-00.000.jsonl"), 0o750)
	case "a file that is not JSON lines":
		_ = os.MkdirAll(folder, 0o750)
		return os.WriteFile(filepath.Join(folder, "calls.jsonl"), []byte("{\"time\":\"2026-11-16T07:00:00Z\",\"event\":\"x\",\"attributes\":{}}\nnot json\n"), 0o600)
	default:
		_ = os.MkdirAll(folder, 0o750)
		return os.WriteFile(filepath.Join(folder, "calls.jsonl"), []byte(`{"time":"2026-11-16T07:00:00Z","event":"hotline.ledger.batch","attributes":{"hotline.batch.format":"hotline.batch.v9"}}`+"\n"), 0o600)
	}
}

// lastSpanID is the span id of the last call line in the folder calls.
func (w *world) lastSpanID() string {
	lines, _ := w.folderLines()
	id := ""
	for _, line := range lines {
		if line["event"] == "hotline.request" {
			id = sprint(line["span_id"])
		}
	}
	return id
}

// folderLines are the lines of calls/calls.jsonl, numbers kept as written.
func (w *world) folderLines() ([]map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(w.dir, "calls", "calls.jsonl"))
	if err != nil {
		return nil, err
	}
	var lines []map[string]any
	for _, raw := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var line map[string]any
		if err := decoder.Decode(&line); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// writeLines writes lines back to calls/calls.jsonl, numbers as they were.
func (w *world) writeLines(lines []map[string]any) error {
	var out bytes.Buffer
	for _, line := range lines {
		data, err := json.Marshal(line)
		if err != nil {
			return err
		}
		out.Write(data)
		out.WriteByte('\n')
	}
	return os.WriteFile(filepath.Join(w.dir, "calls", "calls.jsonl"), out.Bytes(), 0o600)
}

// keepLines keeps only the lines keep says yes to.
func (w *world) keepLines(keep func(map[string]any) bool) error {
	lines, err := w.folderLines()
	if err != nil {
		return err
	}
	var kept []map[string]any
	for _, line := range lines {
		if keep(line) {
			kept = append(kept, line)
		}
	}
	return w.writeLines(kept)
}

// changeLine changes the nth call line, counted from 1, or deletes it when
// change is nil.
func (w *world) changeLine(n int, change func(map[string]any)) error {
	lines, err := w.folderLines()
	if err != nil {
		return err
	}
	calls := 0
	for i, line := range lines {
		if line["event"] != "hotline.request" {
			continue
		}
		if calls++; calls == n {
			if change == nil {
				return w.writeLines(append(lines[:i:i], lines[i+1:]...))
			}
			change(line)
			return w.writeLines(lines)
		}
	}
	return failf("the folder has %d call lines", calls)
}

// changeBatch changes the batch line with a sequence.
func (w *world) changeBatch(sequence int, change func(map[string]any)) error {
	lines, err := w.folderLines()
	if err != nil {
		return err
	}
	for _, line := range lines {
		attrs, _ := line["attributes"].(map[string]any)
		if line["event"] == "hotline.ledger.batch" && fmt.Sprint(attrs["hotline.batch.sequence"]) == strconv.Itoa(sequence) {
			change(attrs)
			return w.writeLines(lines)
		}
	}
	return failf("no batch line has the sequence %d", sequence)
}

// twiceBatch0 writes the batch line with sequence 0 a second time, right
// after itself.
func (w *world) twiceBatch0() error {
	lines, err := w.folderLines()
	if err != nil {
		return err
	}
	for i, line := range lines {
		attrs, _ := line["attributes"].(map[string]any)
		if line["event"] == "hotline.ledger.batch" && fmt.Sprint(attrs["hotline.batch.sequence"]) == "0" {
			lines = slices.Insert(lines, i+1, cloneLine(line))
			return w.writeLines(lines)
		}
	}
	return failf("no batch line has the sequence 0")
}

func cloneLine(line map[string]any) map[string]any {
	data, _ := json.Marshal(line) //nolint:errchkjson // a line read from JSON always encodes again
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var out map[string]any
	_ = decoder.Decode(&out)
	return out
}

// lineRecordHash rebuilds a call line's record hash, the way anyone who
// changed the line would recompute it.
func lineRecordHash(line map[string]any) string {
	attrs := map[string]any{}
	for name, value := range line["attributes"].(map[string]any) {
		if name != "hotline.trace.spans" && !strings.HasPrefix(name, "hotline.content.") {
			attrs[name] = typed(value)
		}
	}
	at, _ := time.Parse(time.RFC3339Nano, sprint(line["time"]))
	r := receivedRecord{attrs: attrs, timestamp: uint64(at.UnixMicro()) * 1000, traceID: sprint(line["trace_id"]), spanID: sprint(line["span_id"])} //nolint:gosec // times after 1970
	return recordHash(r)
}

// typed is a value read with its numbers kept as written, as the record's
// types: a whole number or a decimal.
func typed(v any) any {
	switch x := v.(type) {
	case json.Number:
		if strings.ContainsAny(x.String(), ".eE") {
			f, _ := x.Float64()
			return f
		}
		i, _ := x.Int64()
		return i
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = typed(item)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, item := range x {
			out[k] = typed(item)
		}
		return out
	}
	return v
}

// prints checks that verify printed each piece of text.
func (v verified) prints(texts ...string) error {
	for _, text := range texts {
		if !strings.Contains(v.out, text) {
			return failf("verify did not print %q:\n%s", text, v.out)
		}
	}
	return nil
}

// lines are the lines verify printed that hold text.
func (v verified) lines(text string) []string {
	var out []string
	for _, line := range strings.Split(v.out, "\n") {
		if strings.Contains(line, text) {
			out = append(out, line)
		}
	}
	return out
}

// json is the one JSON object verify printed.
func (v verified) json() (map[string]any, error) {
	var report map[string]any
	if err := json.Unmarshal([]byte(v.out), &report); err != nil {
		return nil, failf("verify printed no JSON object: %v: %s", err, v.out)
	}
	return report, nil
}
