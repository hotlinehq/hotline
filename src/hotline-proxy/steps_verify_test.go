package proxy_test

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math/big"
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
func exporterLedgerSteps(sc *godog.ScenarioContext, sut *ledgerSUT) {
	sc.Step(`^an OTLP exporter and a file exporter$`, func() error {
		sut.fileExport.add("calls", "")
		return nil
	})
	sc.Step(`^the file exporter's queue is full for one record$`, func() error {
		sut.fileExporterOnce()
		sut.fullForEveryThird()
		return nil
	})
	sc.Step(`^the file exporter's queue is full(?: for 1,025 records)?$`, func() error {
		sut.fileExporterOnce()
		sut.fileExport.setExporterLines(0, "      queueMemory: 1\n")
		return nil
	})
	sc.Step(`^the queue has room again$`, func() error { return nil })
	sc.Step(`^each exporter got one batch record, at level INFO, naming itself in hotline.exporter$`, func() error {
		file, err := sut.fileBatches()
		if err != nil {
			return err
		}
		// The stop that settles the run sends one more, empty, last batch.
		otlp := sut.otlp.first().ledger()
		if len(file) == 0 || len(file) != len(otlp) {
			return failf("the file got %d batch records and the OTLP exporter %d", len(file), len(otlp))
		}
		for _, b := range []struct {
			got  receivedRecord
			name string
		}{{file[0], sut.fileExport.folder(0)}, {otlp[0], sut.otlp.first().endpoint}} {
			if b.got.severityText != "INFO" {
				return failf("the batch record of %s is at level %s", b.name, b.got.severityText)
			}
			if err := has(b.got.attrs, "hotline.exporter", b.name); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^both batch records have an empty hotline.batch.dropped_records$`, func() error {
		file, err := sut.fileBatches()
		if err != nil {
			return err
		}
		for _, b := range append(file, sut.otlp.first().ledger()...) {
			if got, ok := b.attrs["hotline.batch.dropped_records"].([]any); !ok || len(got) != 0 {
				return failf("hotline.batch.dropped_records is %v", b.attrs["hotline.batch.dropped_records"])
			}
		}
		return nil
	})
	sc.Step(`^both batch records have the same hotline.batch.id, hotline.batch.root and hotline.batch.certificate$`, func() error {
		file, err := sut.fileBatches()
		if err != nil {
			return err
		}
		otlp := sut.otlp.first().ledger()
		if len(file) == 0 || len(otlp) == 0 {
			return failf("the file got %d batch records and the OTLP exporter %d", len(file), len(otlp))
		}
		for _, name := range []string{"hotline.batch.id", "hotline.batch.root", "hotline.batch.certificate"} {
			if err := has(otlp[0].attrs, name, file[0].attrs[name]); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^each batch record's hotline.batch.signature checks out with the key of its hotline.batch.certificate$`, func() error {
		file, err := sut.fileBatches()
		if err != nil {
			return err
		}
		for _, b := range append(file, sut.otlp.first().ledger()...) {
			if err := checkSigned(b); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the file got 2 records and a batch record at level WARN whose hotline.batch.dropped_records lists the index and hash of the third$`, func() error {
		records, _, err := sut.fileLedger()
		if err != nil {
			return err
		}
		batches, err := sut.fileBatches()
		if err != nil {
			return err
		}
		if len(records) != 2 || len(batches) == 0 {
			return failf("the file got %d records and %d batch records", len(records), len(batches))
		}
		if batches[0].severityText != "WARN" {
			return failf("the batch record is at level %s", batches[0].severityText)
		}
		otlp := sut.otlp.first().got()
		third := otlp[len(otlp)-1].attrs
		want := []any{map[string]any{"index": int64(2), "hash": third["hotline.record.hash"]}}
		if got := batches[0].attrs["hotline.batch.dropped_records"]; fmt.Sprint(got) != fmt.Sprint(want) {
			return failf("hotline.batch.dropped_records is %v, want %v", got, want)
		}
		return nil
	})
	sc.Step(`^the file's batch record's hotline.batch.signature checks out with the key of its hotline.batch.certificate$`, func() error {
		batches, err := sut.fileBatches()
		if err != nil || len(batches) == 0 {
			return failf("the file has no batch record: %v", err)
		}
		return checkSigned(batches[0])
	})
	sc.Step(`^the root rebuilds from the 2 records and the dropped hash$`, func() error {
		records, _, err := sut.fileLedger()
		if err != nil {
			return err
		}
		batches, err := sut.fileBatches()
		if err != nil || len(batches) == 0 {
			return failf("the file has no batch record: %v", err)
		}
		leaves := make([]string, 3)
		for _, r := range records {
			index, _ := r["hotline.batch.index"].(json.Number).Int64()
			leaves[index] = sprint(r["hotline.record.hash"])
		}
		dropped := batches[0].attrs["hotline.batch.dropped_records"].([]any)[0].(map[string]any)
		leaves[2] = sprint(dropped["hash"])
		if root := rootOf(leaves); root != batches[0].attrs["hotline.batch.root"] {
			return failf("the root rebuilds as %s, the batch has %v", root, batches[0].attrs["hotline.batch.root"])
		}
		return nil
	})
	sc.Step(`^the OTLP exporter's batch record lists no dropped record$`, func() error {
		otlp := sut.otlp.first().ledger()
		if len(otlp) == 0 {
			return failf("the OTLP exporter got no batch record")
		}
		if got, ok := otlp[0].attrs["hotline.batch.dropped_records"].([]any); !ok || len(got) != 0 {
			return failf("the OTLP exporter's batch record lists %v", otlp[0].attrs["hotline.batch.dropped_records"])
		}
		return nil
	})
	sc.Step(`^no exporter got a record with event.name hotline.ledger.dropped$`, func() error {
		lines, err := sut.fileExport.folderLines()
		if err != nil {
			return err
		}
		for _, line := range lines {
			if line["event"] == "hotline.ledger.dropped" {
				return failf("the file got a drop record")
			}
		}
		if drops := sut.otlp.first().dropRecords(); len(drops) > 0 {
			return failf("the OTLP exporter got %d drop records", len(drops))
		}
		return nil
	})
	sc.Step(`^the file's batch record has hotline.batch.dropped_count 1025 and no hotline.batch.dropped_records$`, func() error {
		batches, err := sut.fileBatches()
		if err != nil {
			return err
		}
		if len(batches) == 0 {
			return failf("the file got no batch record")
		}
		if fmt.Sprint(batches[0].attrs["hotline.batch.dropped_count"]) != "1025" {
			return failf("hotline.batch.dropped_count is %v", batches[0].attrs["hotline.batch.dropped_count"])
		}
		return hasNo(batches[0].attrs, "hotline.batch.dropped_records")
	})
	sc.Step(`^that batch cannot be rebuilt from the file's copy$`, func() error {
		result := sut.verify(proxy.VerifyOptions{Folder: sut.fileExport.folder(0)})
		if result.code != 1 || !strings.Contains(result.out, "cannot be proven from this copy") {
			return failf("verify gave %d: %s", result.code, result.out)
		}
		return nil
	})
	sc.Step(`^the file got the batch record$`, func() error {
		_, batches, err := sut.fileLedger()
		if err != nil {
			return err
		}
		if len(batches) == 0 {
			return failf("the file got no batch record")
		}
		return nil
	})
	sc.Step(`^the OTLP exporter's collector is busy$`, func() error {
		sut.otlp.first().setBusy(true)
		return nil
	})
	sc.Step(`^the collector is no longer busy$`, func() error {
		sut.otlp.first().setBusy(false)
		return nil
	})
	sc.Step(`^the collector got all (\d+) batch records, in order$`, func(n int) error {
		if _, err := sut.waitBatches(n); err != nil {
			return err
		}
		for i, b := range sut.otlp.first().ledger()[:n] {
			if err := has(b.attrs, "hotline.batch.sequence", int64(i)); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^none of them has hotline.batch.lost_batches$`, func() error {
		for _, b := range sut.otlp.first().ledger() {
			if err := hasNo(b.attrs, "hotline.batch.lost_batches"); err != nil {
				return err
			}
		}
		return nil
	})
	verifySteps(sc, sut)
}

// fileExporterOnce adds a file exporter when the scenario has none yet.
func (sut *ledgerSUT) fileExporterOnce() {
	if sut.fileExport.count() == 0 {
		sut.fileExport.add("calls", "")
	}
}

// fullForEveryThird fills the file exporter's queue for every third call: the
// queue holds 10KiB, and the vendor's third, sixth and later such responses
// have a header of 20KiB, which the record keeps, so that record alone does
// not fit.
func (sut *ledgerSUT) fullForEveryThird() {
	sut.fileExport.setExporterLines(0, "      queueMemory: 10KiB\n")
	var n atomic.Int64
	sut.vendors.vendor("api.shop.test").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		id := "r1"
		if n.Add(1)%3 == 0 {
			id = strings.Repeat("r", 20<<10)
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.Header().Set("X-Request-Id", id)
		_, _ = rw.Write([]byte(`{"orders":[]}`))
	})
}

// twoFilesDropping sends three calls in each of the given batch windows to
// two file exporters. The second always drops the third call of each batch;
// the first drops it too when both is set.
func (sut *ledgerSUT) twoFilesDropping(both bool, batches int) error {
	sut.fullForEveryThird()
	if !both {
		sut.fileExport.setExporterLines(0, "")
	}
	sut.fileExport.add("calls2", "      queueMemory: 10KiB\n")
	for i := range batches {
		if i > 0 {
			sut.proxy.clock.pass(10 * time.Second)
		}
		if err := sut.ordersTimes(3); err != nil {
			return err
		}
	}
	sut.proxy.settle()
	return nil
}

// fileLedger are the attributes of the file exporter's call lines and of
// its batch lines, once the proxy has stopped.
func (sut *ledgerSUT) fileLedger() ([]map[string]any, []map[string]any, error) {
	sut.proxy.settle()
	data, err := os.ReadFile(filepath.Join(sut.fileExport.folder(0), "calls.jsonl"))
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
		switch parsed["event"] {
		case "hotline.ledger.batch":
			batches = append(batches, attrs)
		default:
			records = append(records, attrs)
		}
	}
	return records, batches, nil
}

// verify runs hotline-proxy verify.
func (sut *ledgerSUT) verify(o proxy.VerifyOptions) verified {
	var out bytes.Buffer
	code := proxy.Verify(&out, o)
	return verified{out: out.String(), code: code}
}

// verifySteps are the steps of verify.feature.
func verifySteps(sc *godog.ScenarioContext, sut *ledgerSUT) {
	var result verified
	// changedSpan is the span id of the record a step changed.
	var changedSpan string
	signingSteps(sc, sut, &result)
	sc.Step(`^a file exporter writing to the folder calls$`, func() error {
		sut.fileExport.add("calls", "")
		if sut.apis.integrationCount() == 0 {
			// The folder's calls are checkout's, with the shop spec.
			line, err := sut.apis.shopAPI("", "")
			if err != nil {
				return err
			}
			sut.apis.addIntegration(integration("checkout", line))
			sut.app.appID = "checkout"
		}
		return nil
	})
	sc.Step(`^the file exporter starts a new file past 1 KiB and compresses the old ones$`, func() error {
		sut.fileExport.setExporterLines(0, "      maxFileSize: 1KiB\n      maxFiles: 200\n")
		return nil
	})
	sc.Step(`^the folder calls from a stopped proxy(?:, with (\d+) calls)?(, with calls in 3 batch windows)?$`, func(calls, windows string) error {
		switch {
		case windows != "":
			for range 3 {
				if err := sut.ordersTimes(1); err != nil {
					return err
				}
				sut.proxy.clock.pass(10 * time.Second)
			}
		default:
			n := 1
			_, _ = fmt.Sscan(calls, &n)
			if err := sut.ordersTimes(n); err != nil {
				return err
			}
		}
		sut.proxy.settle()
		return nil
	})
	sc.Step(`^the folder calls from a proxy that was killed$`, func() error {
		// What a killed proxy leaves is its folder as it was while it ran:
		// the folder is copied then, and the proxy writes on elsewhere.
		sut.fileExport.moveFirst("running")
		if err := sut.ordersTimes(2); err != nil {
			return err
		}
		sut.proxy.clock.pass(10 * time.Second)
		if err := sut.ordersTimes(1); err != nil {
			return err
		}
		running := filepath.Join(sut.proxy.dir, "running", "calls.jsonl")
		for deadline := time.Now().Add(waitLimit); ; pause(10 * time.Millisecond) {
			data, _ := os.ReadFile(running) //nolint:gosec // the scenario's own folder
			if bytes.Count(data, []byte("\n")) >= 4 {
				_ = os.MkdirAll(filepath.Join(sut.proxy.dir, "calls"), 0o750)
				return os.WriteFile(filepath.Join(sut.proxy.dir, "calls", "calls.jsonl"), data, 0o600) //nolint:gosec // the scenario's own folder
			}
			if time.Now().After(deadline) {
				return failf("the running proxy wrote %d lines", bytes.Count(data, []byte("\n")))
			}
		}
	})
	sc.Step(`^I run hotline-proxy verify (.+)$`, func(command string) error {
		// The proxy needs no config and no network to verify a folder.
		o, err := sut.verifyOptions(strings.TrimSuffix(command, " with no config and no network"))
		if err != nil {
			return err
		}
		if sut.fileExport.count() == 0 || !strings.Contains(sut.fileExport.folder(0), "running") {
			sut.proxy.settle()
		}
		result = sut.verify(o)
		return nil
	})
	sc.Step(`^the file writes hotline.recorded.shop.order.weight as 2.0 and as 1.5$`, func() error {
		data, err := os.ReadFile(filepath.Join(sut.fileExport.folder(0), "calls.jsonl"))
		if err != nil {
			return err
		}
		for _, want := range []string{`"hotline.recorded.shop.order.weight":2.0`, `"hotline.recorded.shop.order.weight":1.5`} {
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
		entries, _ := os.ReadDir(sut.fileExport.folder(0))
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
		sut.app.appID = "checkout"
		if err := sut.ordersTimes(a); err != nil {
			return err
		}
		sut.app.appID = "refunds"
		for range b {
			if err := sut.app.shopCall("GET", "/refunds", true, "", "", ""); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^only the checkout records and batch records are kept in the folder$`, func() error {
		return sut.keepLines(func(line map[string]any) bool {
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
		return sut.changeLine(2, func(line map[string]any) {
			attrs := line["attributes"].(map[string]any)
			attrs["http.response.status_code"] = json.Number("599")
			changedSpan = sprint(line["span_id"])
			if strings.Contains(change, "recomputed") {
				attrs["hotline.record.hash"] = lineRecordHash(line)
			}
		})
	})
	sc.Step(`^the second record is deleted$`, func() error {
		return sut.changeLine(2, nil)
	})
	sc.Step(`^a copy of the second record with a new span id and recomputed hash is added$`, func() error {
		lines, err := sut.fileExport.folderLines()
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
		return sut.writeLines(lines)
	})
	sc.Step(`^one span is removed from the second line's spans$`, func() error {
		return sut.changeLine(2, func(line map[string]any) {
			attrs := line["attributes"].(map[string]any)
			spans := attrs["hotline.trace.spans"].([]any)
			changedSpan = sprint(line["span_id"])
			attrs["hotline.trace.spans"] = spans[:len(spans)-1]
		})
	})
	sc.Step(`^the end of a span is changed in the second line's spans$`, func() error {
		return sut.changeLine(2, func(line map[string]any) {
			spans := line["attributes"].(map[string]any)["hotline.trace.spans"].([]any)
			changedSpan = sprint(line["span_id"])
			spans[len(spans)-1].(map[string]any)["end_time"] = "2001-01-01T00:00:00.000000Z"
		})
	})
	sc.Step(`^the (span_id|parent_span_id) of the last span in the second line's spans is changed to (the same id in capital letters|zeros, which stand for no span)$`, func(field, id string) error {
		return sut.changeLine(2, func(line map[string]any) {
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
		return sut.changeBatch(0, func(attrs map[string]any) { attrs["hotline.batch.records"] = json.Number("99") })
	})
	sc.Step(`^the batch record with sequence 1 is deleted$`, func() error {
		return sut.keepLines(func(line map[string]any) bool {
			attrs, _ := line["attributes"].(map[string]any)
			return line["event"] != "hotline.ledger.batch" || fmt.Sprint(attrs["hotline.batch.sequence"]) != "1"
		})
	})
	sc.Step(`^batch 2's hotline.batch.previous is changed to the root of batch 0$`, func() error {
		var root any
		if err := sut.changeBatch(0, func(attrs map[string]any) { root = attrs["hotline.batch.root"] }); err != nil {
			return err
		}
		return sut.changeBatch(2, func(attrs map[string]any) { attrs["hotline.batch.previous"] = root })
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
		sut.fullForEveryThird()
		if err := sut.ordersTimes(3); err != nil {
			return err
		}
		sut.proxy.settle()
		return nil
	})
	sc.Step(`^the file exporter dropped one record of batch 0$`, func() error {
		sut.fullForEveryThird()
		if err := sut.ordersTimes(3); err != nil {
			return err
		}
		sut.proxy.settle()
		return nil
	})
	sc.Step(`^the file exporter's queue was full for 1,025 records of batch 0$`, func() error {
		sut.fileExport.setExporterLines(0, "      queueMemory: 1\n")
		if err := sut.ordersTimes(1025); err != nil {
			return err
		}
		sut.proxy.settle()
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
	sc.Step(`^the (time|observed_time|severity|event|trace_id|span_id|flags|body|resource's service.name) of the second record's line is changed$`, func(field string) error {
		return sut.changeLine(2, func(line map[string]any) {
			changeField(line, field)
			changedSpan = sprint(line["span_id"])
		})
	})
	sc.Step(`^two file exporters, (both|only the second) of them dropped one record of batch 0(, of batch 1 and of batch 2)?$`, func(which, more string) error {
		return sut.twoFilesDropping(which == "both", map[bool]int{false: 1, true: 3}[more != ""])
	})
	sc.Step(`^the first folder's record 2 of batch 0 is deleted and its batch record of batch 0 is swapped for the second folder's$`, func() error {
		if err := sut.changeLine(3, nil); err != nil {
			return err
		}
		return sut.swapBatch0()
	})
	sc.Step(`^the first folder's batch record is swapped for the second folder's$`, func() error {
		return sut.swapBatch0()
	})
	sc.Step(`^the second folder's batch record is added to the first folder$`, func() error {
		theirs, err := sut.batch0Of("calls2")
		if err != nil {
			return err
		}
		lines, err := sut.fileExport.folderLines()
		if err != nil {
			return err
		}
		return sut.writeLines(append(lines, theirs))
	})
	sc.Step(`^it prints that the batch record of batch 0 is from the exporter calls2, and this copy's other batch records from calls$`, func() error {
		return result.prints(fmt.Sprintf("the batch record of batch 0 is from the exporter %s, and this copy's other batch records from %s", sut.fileExport.folder(1), sut.fileExport.folder(0)))
	})
	sc.Step(`^it prints that batch 0 has more than one batch record in this copy: one is from the exporter calls2$`, func() error {
		return result.prints("batch 0 has more than one batch record in this copy: one is from the exporter " + sut.fileExport.folder(1))
	})
	sc.Step(`^it prints that 1 problem was found$`, func() error {
		return result.prints("1 problem found")
	})
	sc.Step(`^it prints that the batch record of batch 0 lists index 2, which this copy holds$`, func() error {
		return result.prints("the batch record of batch 0 lists index 2, which this copy holds")
	})
	sc.Step(`^it prints that the folder is the copy of the exporter calls$`, func() error {
		return result.prints("the folder is the copy of the exporter " + sut.fileExport.folder(0))
	})
	sc.Step(`^its summary names the exporter calls$`, func() error {
		report, err := result.json()
		if err != nil {
			return err
		}
		summary, _ := report["summary"].(map[string]any)
		if summary["exporter"] != sut.fileExport.folder(0) {
			return failf("the summary names the exporter %v", summary["exporter"])
		}
		return nil
	})
	sc.Step(`^the first batch record's (certificate is cut short|certificate is signed by another key|certificate is swapped for one of another key|record is signed again with a certificate naming the service another-proxy|record is signed again with a certificate whose hour ended before it)$`, func(change string) error {
		return sut.changeBatchLine(0, func(line map[string]any) {
			attrs := line["attributes"].(map[string]any)
			at, _ := time.Parse(time.RFC3339Nano, sprint(line["time"]))
			name := "hotline-proxy"
			switch change {
			case "certificate is cut short":
				attrs["hotline.batch.certificate"] = sprint(attrs["hotline.batch.certificate"])[:40]
			case "certificate is signed by another key":
				attrs["hotline.batch.certificate"], _ = testCertificate(name, at.Add(-time.Minute), at.Add(time.Hour), false)
			case "certificate is swapped for one of another key":
				attrs["hotline.batch.certificate"], _ = testCertificate(name, at.Add(-time.Minute), at.Add(time.Hour), true)
			default:
				from, to := at.Add(-time.Minute), at.Add(time.Hour)
				if strings.HasSuffix(change, "ended before it") {
					from, to = at.Add(-2*time.Hour), at.Add(-time.Hour)
				} else {
					name = "another-proxy"
				}
				var private ed25519.PrivateKey
				attrs["hotline.batch.certificate"], private = testCertificate(name, from, to, true)
				digest := signedDigest(lineAsReceived(line))
				attrs["hotline.batch.signature"] = "ed25519:" + base64.StdEncoding.EncodeToString(ed25519.Sign(private, digest[:]))
			}
		})
	})
	sc.Step(`^it prints that (the certificate of batch 0 cannot be read|the certificate of batch 0 is not signed by its own key|the certificate of batch 0 names the service another-proxy, and the batch record hotline-proxy|batch 0 was closed outside its certificate's hour)$`, func(message string) error {
		return result.prints(message)
	})
	sc.Step(`^the flags of the first span in the second line's spans are changed$`, func() error {
		return sut.changeLine(2, func(line map[string]any) {
			spans := line["attributes"].(map[string]any)["hotline.trace.spans"].([]any)
			changedSpan = sprint(line["span_id"])
			first := spans[0].(map[string]any)
			first["flags"] = json.Number(strconv.FormatInt(typed(first["flags"]).(int64)^1, 10))
		})
	})
	sc.Step(`^the (time|observed_time|severity|resource's service.name|hotline.exporter|hotline.batch.dropped_records) of the first batch record's line is changed$`, func(field string) error {
		return sut.changeBatchLine(0, func(line map[string]any) { changeField(line, field) })
	})
	sc.Step(`^the first batch record's hotline.batch.format is changed to (hotline.batch.v\d)$`, func(format string) error {
		return sut.changeBatch(0, func(attrs map[string]any) { attrs["hotline.batch.format"] = format })
	})
	sc.Step(`^it prints that the folder was written by an older edge build \((hotline.batch.v\d)\), which this verify cannot check$`, func(format string) error {
		return result.prints("was written by an older edge build (" + format + "), which this verify cannot check")
	})
	sc.Step(`^the second record is deleted and listed in the batch record's hotline.batch.dropped_records, (with the signature left as it was|signed with a key of its own, keeping the batch's key)$`, func(how string) error {
		return sut.fakeDrop(how != "with the signature left as it was")
	})
	sc.Step(`^the vendor answered GET /orders with the json body (.+)$`, func(body string) error {
		sut.vendors.vendor("api.shop.test").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(body))
		})
		if err := sut.app.shopCall("GET", "/orders", true, "", "", ""); err != nil {
			return err
		}
		sut.proxy.settle()
		return nil
	})
	sc.Step(`^the body was saved as (\S+)$`, func(name string) error {
		sut.proxy.writeFile(name, string(sut.app.respBody))
		return nil
	})
	sc.Step(`^it prints that the response body matches the record's hotline.response.body.hash$`, func() error {
		return result.prints("the response body matches the record's hotline.response.body.hash")
	})
	sc.Step(`^it prints that the response body does not match$`, func() error {
		return result.prints("the response body does not match")
	})
	sc.Step(`^the integration has content full and maxBodySize (\S+)$`, func(size string) error {
		line, err := sut.apis.shopAPI("", "")
		if err != nil {
			return err
		}
		sut.apis.onlyIntegration(integration("checkout", line) + "    content: full\n    maxBodySize: " + size + "\n")
		sut.app.appID = "checkout"
		return nil
	})
	sc.Step(`^the app sends POST /orders with a json body of 3 KiB$`, func() error {
		body := `{"item":"` + strings.Repeat("A", 3<<10) + `","quantity":2}`
		return sut.app.shopCall("POST", "/orders", true, "Idempotency-Key: k1", "json", body)
	})
	sc.Step(`^it prints that 1 kept body matches its hash$`, func() error {
		if result.code != 0 {
			return failf("verify gave %d: %s", result.code, result.out)
		}
		return result.prints("kept bodies match their hash")
	})
	sc.Step(`^it prints that (\d+) kept bodies match their hash$`, func(n int) error {
		return result.prints(fmt.Sprintf("%d kept bodies match their hash", n))
	})
	sc.Step(`^it prints that the request body kept with the record does not match its hash$`, func() error {
		return result.prints("the request body kept with the record with span id", "does not match its hash")
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
	sc.Step(`^(http.response.status_code is changed in a record|a span's end is changed in a record's trace|a record is changed and its hash recomputed|a record is deleted|hotline.batch.records is changed in a batch record|batch 2's hotline.batch.previous is changed|a record's hotline.batch.index is changed to 99|a changed batch 0 is in the file twice|a changed record is in the file twice|batch 0 names a batch before it)$`, func(change string) error {
		switch change {
		case "a record's hotline.batch.index is changed to 99":
			return sut.changeLine(2, func(line map[string]any) {
				line["attributes"].(map[string]any)["hotline.batch.index"] = json.Number("99")
			})
		case "a changed batch 0 is in the file twice":
			return sut.twiceBatch0(func(attrs map[string]any) { attrs["hotline.batch.records"] = json.Number("99") })
		case "a changed record is in the file twice":
			return sut.twiceRecord(func(attrs map[string]any) { attrs["http.response.status_code"] = json.Number("599") })
		case "batch 0 names a batch before it":
			return sut.changeBatch(0, func(attrs map[string]any) { attrs["hotline.batch.previous"] = "sha256:" + strings.Repeat("0", 64) })
		case "http.response.status_code is changed in a record":
			return sut.changeLine(2, func(line map[string]any) {
				line["attributes"].(map[string]any)["http.response.status_code"] = json.Number("599")
			})
		case "a span's end is changed in a record's trace":
			return sut.changeLine(2, func(line map[string]any) {
				spans := line["attributes"].(map[string]any)["hotline.trace.spans"].([]any)
				spans[len(spans)-1].(map[string]any)["end_time"] = "2001-01-01T00:00:00.000000Z"
			})
		case "a record is changed and its hash recomputed":
			return sut.changeLine(2, func(line map[string]any) {
				attrs := line["attributes"].(map[string]any)
				attrs["http.response.status_code"] = json.Number("599")
				attrs["hotline.record.hash"] = lineRecordHash(line)
			})
		case "a record is deleted":
			return sut.changeLine(2, nil)
		case "hotline.batch.records is changed in a batch record":
			return sut.changeBatch(0, func(attrs map[string]any) { attrs["hotline.batch.records"] = json.Number("99") })
		default:
			var root any
			if err := sut.changeBatch(0, func(attrs map[string]any) { root = attrs["hotline.batch.root"] }); err != nil {
				return err
			}
			return sut.changeBatch(2, func(attrs map[string]any) { attrs["hotline.batch.previous"] = root })
		}
	})
	sc.Step(`^the kept (url.full|request body) is changed(?: in the record)?$`, func(what string) error {
		return sut.changeLine(1, func(line map[string]any) {
			attrs := line["attributes"].(map[string]any)
			if what == "url.full" {
				attrs["hotline.content.url.full"] = "https://changed.example/"
				return
			}
			attrs["hotline.content.request.body"] = base64.StdEncoding.EncodeToString([]byte("{}"))
		})
	})
	sc.Step(`^the checkout integration's shop spec has the overlay (\S+)$`, func(name string) error {
		line, err := sut.apis.shopAPI("", "    overlays: [./specs/"+name+"]\n")
		sut.apis.onlyIntegration(integration("checkout", line))
		return err
	})
	var namedBatch string
	sc.Step(`^the first batch record loses its hotline.batch.sequence$`, func() error {
		lines, err := sut.fileExport.folderLines()
		if err != nil {
			return err
		}
		for _, line := range lines {
			if line["event"] == "hotline.ledger.batch" {
				attrs := line["attributes"].(map[string]any)
				namedBatch = sprint(attrs["hotline.batch.id"])
				delete(attrs, "hotline.batch.sequence")
				return sut.writeLines(lines)
			}
		}
		return failf("the folder has no batch line")
	})
	sc.Step(`^it prints that the signature of that batch, named by its id, does not check out$`, func() error {
		return result.prints("the signature of batch " + namedBatch + " does not check out")
	})
	sc.Step(`^it prints the instance checkout-0 with the time its run started$`, func() error {
		return result.prints("instance checkout-0, started at 20")
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
		line, err := sut.apis.shopAPI("", "")
		if err != nil {
			return err
		}
		sut.apis.onlyIntegration(integration("checkout", line) + "    content: full\n    maxBodySize: 1KiB\n")
		body := `{"item":"` + strings.Repeat("A", 3<<10) + `","quantity":2}`
		if err := sut.app.shopCall("POST", "/orders", true, "Idempotency-Key: k1", "json", body); err != nil {
			return err
		}
		sut.proxy.settle()
		return nil
	})
	sc.Step(`^the folder calls holds (a record|batch 0) twice$`, func(what string) error {
		if err := sut.ordersTimes(3); err != nil {
			return err
		}
		sut.proxy.settle()
		if what == "batch 0" {
			return sut.twiceBatch0(func(map[string]any) {})
		}
		return sut.twiceRecord(func(map[string]any) {})
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
		if err := sut.ordersTimes(1); err != nil {
			return err
		}
		sut.proxy.settle()
		return nil
	})
	sc.Step(`^that record was changed$`, func() error {
		return sut.changeLine(1, func(line map[string]any) {
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
		if body["side"] != "response" || body["matches"] != true || body["span_id"] != sut.lastSpanID() {
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
func (sut *ledgerSUT) verifyOptions(command string) (proxy.VerifyOptions, error) {
	var o proxy.VerifyOptions
	rest := command
	for _, bad := range []string{"a folder that does not exist", "a file that is not JSON lines", "a batch record of format v9",
		"a line with no time", "a compressed file that is not gzip", "a cut-off compressed file", "a rotated file that is a folder"} {
		if after, found := strings.CutPrefix(command, bad); found {
			rest = "calls" + after
			if err := sut.badInput(bad); err != nil {
				return o, err
			}
		}
	}
	words := strings.Fields(rest)
	o.Folder = filepath.Join(sut.proxy.dir, words[0])
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
			o.Body, i = filepath.Join(sut.proxy.dir, next), i+1
			if _, err := os.Stat(o.Body); err != nil {
				sut.proxy.writeFile(next, `{"orders":["another"]}`)
			}
		case "--span":
			// The zero span id stands for one no call has.
			o.Span, i = sut.lastSpanID(), i+1
			if next == "0000000000000000" {
				o.Span = next
			}
		case "--side":
			o.Side, i = next, i+1
		case "--root-authority":
			o.RootAuthorities, i = append(o.RootAuthorities, filepath.Join(sut.proxy.dir, next)), i+1
		}
	}
	return o, nil
}

// badInput makes the folder calls hold input verify cannot read.
func (sut *ledgerSUT) badInput(what string) error {
	folder := filepath.Join(sut.proxy.dir, "calls")
	switch what {
	case "a folder that does not exist":
		sut.fileExport.removeAll()
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
func (sut *ledgerSUT) lastSpanID() string {
	lines, _ := sut.fileExport.folderLines()
	id := ""
	for _, line := range lines {
		if line["event"] == "hotline.request" {
			id = sprint(line["span_id"])
		}
	}
	return id
}

// folderLines are the lines of calls/calls.jsonl, numbers kept as written.
func (sut *fileExportSUT) folderLines() ([]map[string]any, error) { return sut.linesIn("calls") }

// linesIn are the lines of calls.jsonl in a folder of the scenario's,
// numbers kept as written.
func (sut *fileExportSUT) linesIn(folder string) ([]map[string]any, error) {
	// The folder is read through a root, so the name cannot leave the
	// scenario's own folder.
	root, err := os.OpenRoot(sut.proxy.dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadFile(filepath.Join(folder, "calls.jsonl"))
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
func (sut *ledgerSUT) writeLines(lines []map[string]any) error {
	var out bytes.Buffer
	for _, line := range lines {
		data, err := json.Marshal(line)
		if err != nil {
			return err
		}
		out.Write(data)
		out.WriteByte('\n')
	}
	return os.WriteFile(filepath.Join(sut.proxy.dir, "calls", "calls.jsonl"), out.Bytes(), 0o600)
}

// keepLines keeps only the lines keep says yes to.
func (sut *ledgerSUT) keepLines(keep func(map[string]any) bool) error {
	lines, err := sut.fileExport.folderLines()
	if err != nil {
		return err
	}
	var kept []map[string]any
	for _, line := range lines {
		if keep(line) {
			kept = append(kept, line)
		}
	}
	return sut.writeLines(kept)
}

// changeLine changes the nth call line, counted from 1, or deletes it when
// change is nil.
func (sut *ledgerSUT) changeLine(n int, change func(map[string]any)) error {
	lines, err := sut.fileExport.folderLines()
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
				return sut.writeLines(append(lines[:i:i], lines[i+1:]...))
			}
			change(line)
			return sut.writeLines(lines)
		}
	}
	return failf("the folder has %d call lines", calls)
}

// changeBatch changes the attributes of the batch line with a sequence.
func (sut *ledgerSUT) changeBatch(sequence int, change func(map[string]any)) error {
	return sut.changeBatchLine(sequence, func(line map[string]any) { change(line["attributes"].(map[string]any)) })
}

// changeBatchLine changes the whole batch line with a sequence.
func (sut *ledgerSUT) changeBatchLine(sequence int, change func(map[string]any)) error {
	lines, err := sut.fileExport.folderLines()
	if err != nil {
		return err
	}
	for _, line := range lines {
		attrs, _ := line["attributes"].(map[string]any)
		if line["event"] == "hotline.ledger.batch" && fmt.Sprint(attrs["hotline.batch.sequence"]) == strconv.Itoa(sequence) {
			change(line)
			return sut.writeLines(lines)
		}
	}
	return failf("no batch line has the sequence %d", sequence)
}

// twiceBatch0 writes the batch line with sequence 0 a second time, right
// after itself, with change made to the copy's attributes.
func (sut *ledgerSUT) twiceBatch0(change func(attrs map[string]any)) error {
	return sut.twice(func(line map[string]any, attrs map[string]any) bool {
		return line["event"] == "hotline.ledger.batch" && fmt.Sprint(attrs["hotline.batch.sequence"]) == "0"
	}, change)
}

// twiceRecord writes the first call line a second time, right after
// itself, with change made to the copy's attributes.
func (sut *ledgerSUT) twiceRecord(change func(attrs map[string]any)) error {
	return sut.twice(func(line map[string]any, _ map[string]any) bool { return line["event"] == "hotline.request" }, change)
}

// twice writes the first line that is wanted a second time, right after
// itself, with change made to the copy's attributes.
func (sut *ledgerSUT) twice(wanted func(line, attrs map[string]any) bool, change func(attrs map[string]any)) error {
	lines, err := sut.fileExport.folderLines()
	if err != nil {
		return err
	}
	for i, line := range lines {
		attrs, _ := line["attributes"].(map[string]any)
		if wanted(line, attrs) {
			copied := cloneLine(line)
			change(copied["attributes"].(map[string]any))
			lines = slices.Insert(lines, i+1, copied)
			return sut.writeLines(lines)
		}
	}
	return failf("no line to copy")
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
func lineRecordHash(line map[string]any) string { return recordHash(lineAsReceived(line)) }

// lineAsReceived is a file line as the record it was written from, its
// attributes typed, the spans and the content: full values left out.
func lineAsReceived(line map[string]any) receivedRecord {
	attrs := map[string]any{}
	for name, value := range line["attributes"].(map[string]any) {
		if name != "hotline.trace.spans" && !strings.HasPrefix(name, "hotline.content.") {
			attrs[name] = typed(value)
		}
	}
	micros := func(field string) uint64 {
		t, _ := time.Parse(time.RFC3339Nano, sprint(line[field]))
		return uint64(t.UnixMicro()) * 1000
	}
	flags, _ := strconv.ParseUint(text(line["flags"]), 16, 32)
	resource, _ := typed(line["resource"]).(map[string]any)
	return receivedRecord{
		attrs: attrs, timestamp: micros("time"), observed: micros("observed_time"),
		traceID: text(line["trace_id"]), spanID: text(line["span_id"]), flags: uint32(flags),
		severityText: text(line["severity"]), severityNumber: severityNumber(text(line["severity"])),
		eventName: text(line["event"]), body: text(line["body"]), resource: resource,
	}
}

// text is a line's text field, empty when it has none.
func text(v any) string {
	s, _ := v.(string)
	return s
}

// changeField changes one field of a line, as someone who edits the file
// would.
func changeField(line map[string]any, field string) {
	switch field {
	case "time", "observed_time":
		at, _ := time.Parse(time.RFC3339Nano, sprint(line[field]))
		line[field] = at.Add(time.Microsecond).UTC().Format("2006-01-02T15:04:05.000000Z07:00")
	case "severity":
		line[field] = map[bool]string{true: "WARN", false: "INFO"}[line[field] == "INFO"]
	case "event":
		line[field] = "hotline.probe"
	case "trace_id":
		line[field] = strings.Repeat("ab", 16)
	case "span_id":
		line[field] = "00000000000000ab"
	case "hotline.exporter":
		line["attributes"].(map[string]any)[field] = "another-exporter"
	case "hotline.batch.dropped_records":
		line["attributes"].(map[string]any)[field] = []any{}
	case "flags":
		line[field] = map[bool]string{true: "00", false: "01"}[line[field] == "01"]
	case "body":
		line[field] = "a note"
	case "resource's service.name":
		line["resource"].(map[string]any)["service.name"] = "another-proxy"
	}
}

// fileBatches are the batch records of the file exporter's folder.
func (sut *ledgerSUT) fileBatches() ([]receivedRecord, error) {
	sut.proxy.settle()
	lines, err := sut.fileExport.folderLines()
	if err != nil {
		return nil, err
	}
	var batches []receivedRecord
	for _, line := range lines {
		if line["event"] == "hotline.ledger.batch" {
			batches = append(batches, lineAsReceived(line))
		}
	}
	return batches, nil
}

// batch0Of is the batch line with sequence 0 in a folder of the scenario's.
func (sut *ledgerSUT) batch0Of(folder string) (map[string]any, error) {
	lines, err := sut.fileExport.linesIn(folder)
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		attrs, _ := line["attributes"].(map[string]any)
		if line["event"] == "hotline.ledger.batch" && fmt.Sprint(attrs["hotline.batch.sequence"]) == "0" {
			return line, nil
		}
	}
	return nil, failf("the folder %s has no batch 0", folder)
}

// swapBatch0 puts the second folder's batch line of batch 0 in place of the
// first folder's.
func (sut *ledgerSUT) swapBatch0() error {
	theirs, err := sut.batch0Of("calls2")
	if err != nil {
		return err
	}
	return sut.changeBatchLine(0, func(line map[string]any) {
		clear(line)
		maps.Copy(line, theirs)
	})
}

// fakeDrop deletes the second call line and lists it in the batch record's
// hotline.batch.dropped_records, as someone who wants the deletion to pass
// would: with the signature left as it was, or signed again with a key of
// their own while the batch record still names the proxy's key.
func (sut *ledgerSUT) fakeDrop(ownKey bool) error {
	var record map[string]any
	if err := sut.changeLine(2, func(line map[string]any) { record = line }); err != nil {
		return err
	}
	if err := sut.changeLine(2, nil); err != nil {
		return err
	}
	attrs := record["attributes"].(map[string]any)
	return sut.changeBatchLine(0, func(batch map[string]any) {
		batchAttrs := batch["attributes"].(map[string]any)
		batchAttrs["hotline.batch.dropped_records"] = []any{map[string]any{"index": attrs["hotline.batch.index"], "hash": attrs["hotline.record.hash"]}}
		if ownKey {
			_, private, _ := ed25519.GenerateKey(rand.Reader)
			digest := signedDigest(lineAsReceived(batch))
			batchAttrs["hotline.batch.signature"] = "ed25519:" + base64.StdEncoding.EncodeToString(ed25519.Sign(private, digest[:]))
		}
	})
}

// testCertificate is a certificate for a new Ed25519 key, naming the
// service name and valid from from to to, as base64 of its DER bytes, and
// the key. Signed by that key itself, or by another key when self is
// false.
func testCertificate(name string, from, to time.Time, self bool) (string, ed25519.PrivateKey) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	signer := private
	if !self {
		_, signer, _ = ed25519.GenerateKey(rand.Reader)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: from, NotAfter: to}
	der, _ := x509.CreateCertificate(rand.Reader, template, template, public, signer)
	return base64.StdEncoding.EncodeToString(der), private
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
