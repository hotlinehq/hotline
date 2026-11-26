package proxy_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// ledgerSteps are the steps of ledger.feature: the hashes every record
// carries, and the signed batches they are grouped in. Every check rebuilds
// what it checks from what an exporter got, the way anyone without a key
// would (docs/proxy-ledger.md).
func ledgerSteps(sc *godog.ScenarioContext, w *world) {
	var changed receivedRecord
	var changedTrace callTrace
	// killed are the batch records sent before the proxy was killed.
	var killed []receivedRecord
	// before is a batch record of the first run, for scenarios that restart.
	var before map[string]any

	sc.Step(`^the proxy's clock is a test clock, so a wait of minutes passes at once$`, func() error {
		if w.clock == nil {
			w.clock = &fakeClock{}
		}
		return nil
	})
	sc.Step(`^(\d+) seconds pass(?: with no (?:more )?calls)?$`, func(seconds int) error {
		if err := w.start(); err != nil {
			return err
		}
		// Each call's record is made in the background: let it join its
		// batch before the window closes.
		w.waitCalls(w.sent)
		w.clock.pass(time.Duration(seconds) * time.Second)
		return nil
	})
	sc.Step(`^an hour passes on the proxy's clock$`, func() error {
		w.waitCalls(w.sent)
		w.clock.pass(time.Hour)
		return nil
	})
	sc.Step(`^the app sends GET /orders (\d+) times$`, func(n int) error {
		return w.ordersTimes(n)
	})
	sc.Step(`^the app sends GET /orders twice$`, func() error {
		return w.ordersTimes(2)
	})
	sc.Step(`^the app sends GET /orders 1,025 times$`, func() error {
		return w.ordersTimes(1025)
	})
	sc.Step(`^the app sends GET /orders in each of (\d+) batch windows$`, func(n int) error {
		for range n {
			if err := w.ordersTimes(1); err != nil {
				return err
			}
			w.clock.pass(10 * time.Second)
		}
		return nil
	})
	sc.Step(`^the app sends GET /orders with no integration ID$`, func() error {
		w.appID = ""
		return w.shopCall("GET", "/orders", true, "", "", "")
	})
	sc.Step(`^the app sends POST /orders with no integration ID and a json body$`, func() error {
		w.appID = ""
		return w.shopCall("POST", "/orders", true, "Idempotency-Key: k1", "json", `{"item":"A1","quantity":2}`)
	})
	sc.Step(`^the record has no hotline.signature or hotline.signature_key_id$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		return hasNo(record, "hotline.signature", "hotline.signature_key_id")
	})
	sc.Step(`^the record has (hotline\.[\w.]+)$`, func(name string) error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		return hasAll(record, name)
	})
	sc.Step(`^the record has hotline.record.hash, hotline.batch.id and hotline.batch.index$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		return hasAll(record, "hotline.record.hash", "hotline.batch.id", "hotline.batch.index")
	})
	sc.Step(`^the record has no hotline.request.body.hash or hotline.response.body.hash$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		return hasNo(record, "hotline.request.body.hash", "hotline.response.body.hash")
	})
	sc.Step(`^(?:it has|the record has) hotline.request.body.hash and hotline.response.body.hash$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		return hasAll(record, "hotline.request.body.hash", "hotline.response.body.hash")
	})
	sc.Step(`^the record is minimal$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		return hasNo(record, "url.template", "hotline.schema.status")
	})
	sc.Step(`^the hash checks out(?: when rebuilt from the record alone)?$`, func() error {
		if changed.attrs != nil {
			return checkRecordHash(changed)
		}
		records := w.records()
		if len(records) == 0 {
			return failf("no record arrived")
		}
		return checkRecordHash(records[len(records)-1])
	})
	sc.Step(`^(.+) in the received record$`, func(change string) error {
		records := w.records()
		if len(records) == 0 {
			return failf("no record arrived")
		}
		last := records[len(records)-1]
		changed = last
		changed.attrs = maps.Clone(last.attrs)
		recompute := false
		if before, found := strings.CutSuffix(change, " and hotline.record.hash recomputed"); found {
			change, recompute = before, true
		}
		switch change {
		case "http.response.status_code is changed":
			changed.attrs["http.response.status_code"] = changed.attrs["http.response.status_code"].(int64) + 1
		case "url.full is changed":
			changed.attrs["url.full"] = "https://api.shop.test/orders?x=1"
		case "the trace_id is changed":
			changed.traceID = strings.Repeat("ab", 16)
		case "the timestamp is changed":
			changed.timestamp += 1000
		case "the observed time is changed":
			changed.observed += 1000
		case "the level is changed":
			changed.severityNumber, changed.severityText = 13, "WARN"
		case "the event name is changed":
			changed.eventName = "hotline.probe"
		case "the flags are changed":
			changed.flags ^= 1
		case "a body is added":
			changed.body = "a note"
		case "a resource label is changed":
			changed.resource = maps.Clone(changed.resource)
			changed.resource["service.name"] = "another-proxy"
		case "hotline.request.body.hash is changed", "hotline.trace.hash is changed":
			name := strings.TrimSuffix(change, " is changed")
			changed.attrs[name] = "sha256:" + strings.Repeat("00", 32)
		case "hotline.batch.index is changed":
			changed.attrs["hotline.batch.index"] = changed.attrs["hotline.batch.index"].(int64) + 1
		case "shop.order.item is changed":
			changed.attrs["shop.order.item"] = "A2"
		case "an attribute is added":
			changed.attrs["shop.added"] = "x"
		case "an attribute is removed":
			delete(changed.attrs, "hotline.ingress")
		default:
			return godog.ErrPending
		}
		if recompute {
			changed.attrs["hotline.record.hash"] = recordHash(changed)
		}
		return nil
	})
	sc.Step(`^every LogRecord field the receiver got, on the call record and the batch record, is one the hash or signature covers, in a scope named hotline-proxy$`, func() error {
		records := w.records()
		batches, err := w.waitBatches(1)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			return failf("no record arrived")
		}
		for _, r := range append(records[len(records)-1:], batches...) {
			for _, field := range r.fields {
				if !slices.Contains(coveredFields(), field) {
					return failf("the %s record has LogRecord field %d, which its hash does not cover", r.eventName, field)
				}
			}
			if r.unknown > 0 {
				return failf("the %s record has %d bytes of fields OTLP does not name", r.eventName, r.unknown)
			}
			if r.scope != "hotline-proxy" {
				return failf("the %s record came in the scope %q", r.eventName, r.scope)
			}
		}
		return checkBatchSignature(batches[0])
	})
	sc.Step(`^the hash no longer checks out$`, func() error {
		if checkRecordHash(changed) == nil {
			return failf("the changed record still checks out")
		}
		return nil
	})
	sc.Step(`^the batch's root no longer rebuilds from its records$`, func() error {
		batch, err := w.batchOf(changed)
		if err != nil {
			return err
		}
		records := w.records()
		var leaves []string
		for _, r := range records {
			if r.attrs["hotline.batch.id"] == changed.attrs["hotline.batch.id"] {
				if r.spanID == changed.spanID {
					r = changed
				}
				leaves = append(leaves, sprint(r.attrs["hotline.record.hash"]))
			}
		}
		if rootOf(leaves) == batch.attrs["hotline.batch.root"] {
			return failf("the batch's root still rebuilds")
		}
		return nil
	})
	sc.Step(`^hotline.request.body.hash is the body hash of the body the vendor got, with the record's trace id and span id$`, func() error {
		calls := w.vendor("api.shop.test").received()
		if len(calls) == 0 {
			return failf("the vendor got no call")
		}
		return w.bodyHashIs("request", calls[len(calls)-1].body)
	})
	sc.Step(`^hotline.response.body.hash is the body hash of the body the app got, with the record's trace id and span id$`, func() error {
		return w.bodyHashIs("response", w.respBody)
	})
	sc.Step(`^hotline.response.body.hash is the body hash of those 1000 bytes$`, func() error {
		return w.bodyHashIs("response", []byte(strings.Repeat("x", 1000)))
	})
	sc.Step(`^hotline.request.body.hash and hotline.response.body.hash are both there$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		return hasAll(record, "hotline.request.body.hash", "hotline.response.body.hash")
	})
	sc.Step(`^each is the body hash of zero bytes, with the record's trace id and span id$`, func() error {
		if err := w.bodyHashIs("request", nil); err != nil {
			return err
		}
		return w.bodyHashIs("response", nil)
	})
	sc.Step(`^the vendor answers (\d+) with no body$`, func(status int) error {
		w.vendor("api.shop.test").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			rw.WriteHeader(status)
		})
		return nil
	})
	sc.Step(`^the vendor closes the connection after 1000 of 5000 promised bytes$`, func() error {
		w.vendor("api.shop.test").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			rw.Header().Set("Content-Length", "5000")
			rw.WriteHeader(http.StatusOK)
			_, _ = rw.Write([]byte(strings.Repeat("x", 1000)))
			if f, ok := rw.(http.Flusher); ok {
				f.Flush()
			}
			panic(http.ErrAbortHandler)
		})
		return nil
	})
	sc.Step(`^the two records have different hotline.response.body.hash$`, func() error {
		records := w.records()
		if len(records) != 2 {
			return failf("want two records, got %d", len(records))
		}
		if records[0].attrs["hotline.response.body.hash"] == records[1].attrs["hotline.response.body.hash"] {
			return failf("both records have %v", records[0].attrs["hotline.response.body.hash"])
		}
		return nil
	})
	sc.Step(`^the trace hash checks out when rebuilt from the spans alone$`, func() error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		return checkTraceHash(t)
	})
	sc.Step(`^(.+) in the received trace$`, func(change string) error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		changedTrace = callTrace{record: t.record}
		for _, s := range t.spans {
			s.attrs = maps.Clone(s.attrs)
			changedTrace.spans = append(changedTrace.spans, s)
		}
		switch change {
		case "a span's end is changed":
			changedTrace.spans[len(changedTrace.spans)-1].end += 1000
		default:
			return godog.ErrPending
		}
		return nil
	})
	sc.Step(`^the trace hash no longer checks out$`, func() error {
		if checkTraceHash(changedTrace) == nil {
			return failf("the changed trace still checks out")
		}
		return nil
	})
	sc.Step(`^the record holds no (\S+)$`, func(secret string) error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		// Hashes and ids are random-looking hex, which can hold any short
		// text by chance, so they are left out of the search.
		for name, value := range record {
			if !strings.HasSuffix(name, "hash") && name != "hotline.batch.id" && strings.Contains(fmt.Sprint(value), secret) {
				return failf("the record holds %s in %s: %v", secret, name, record)
			}
		}
		return nil
	})
	sc.Step(`^the records have the same hotline.batch.id, a UUID version 7$`, func() error {
		records := w.records()
		if len(records) == 0 {
			return failf("no record arrived")
		}
		id := sprint(records[0].attrs["hotline.batch.id"])
		for _, r := range records {
			if r.attrs["hotline.batch.id"] != id {
				return failf("the records are in batches %v and %v", id, r.attrs["hotline.batch.id"])
			}
		}
		if len(id) != 36 || id[14] != '7' || !strings.ContainsAny(id[19:20], "89ab") {
			return failf("%s is not a UUID version 7", id)
		}
		return nil
	})
	sc.Step(`^their hotline.batch.index are 0, 1 and 2, in the order they were written$`, func() error {
		records := w.records()
		sort.Slice(records, func(i, j int) bool { return records[i].timestamp < records[j].timestamp })
		for i, r := range records {
			if r.attrs["hotline.batch.index"] != int64(i) {
				return failf("record %d has index %v", i, r.attrs["hotline.batch.index"])
			}
		}
		return nil
	})
	sc.Step(`^the exporter got a log record with event.name hotline.ledger.batch$`, func() error {
		_, err := w.waitBatches(1)
		return err
	})
	sc.Step(`^it has hotline.batch.format hotline.batch.v2, hotline.batch.records (\d+) and hotline.batch.root$`, func(n int64) error {
		batches, err := w.waitBatches(1)
		if err != nil {
			return err
		}
		b := batches[0].attrs
		if err := has(b, "hotline.batch.format", "hotline.batch.v2"); err != nil {
			return err
		}
		if err := has(b, "hotline.batch.records", n); err != nil {
			return err
		}
		return hasAll(b, "hotline.batch.root")
	})
	sc.Step(`^the root rebuilds from the (\d+) records' hashes$`, func(n int) error {
		batches, err := w.waitBatches(1)
		if err != nil {
			return err
		}
		records, err := w.waitRecords(n)
		if err != nil {
			return err
		}
		return checkRoot(batches[0], records, nil)
	})
	sc.Step(`^hotline.batch.signature checks out with hotline.batch.key$`, func() error {
		batches, err := w.waitBatches(1)
		if err != nil {
			return err
		}
		return checkBatchSignature(batches[0])
	})
	sc.Step(`^ledger batches hold at most (\d+) records$`, func(n int) error {
		w.topExtra += fmt.Sprintf("ledger:\n  batch:\n    maxRecords: %d\n", n)
		return nil
	})
	sc.Step(`^the first 3 records are in one batch and the 4th in the next$`, func() error {
		records := w.records()
		if len(records) != 4 {
			return failf("want 4 records, got %d", len(records))
		}
		sort.Slice(records, func(i, j int) bool { return records[i].timestamp < records[j].timestamp })
		first := records[0].attrs["hotline.batch.id"]
		for _, r := range records[1:3] {
			if r.attrs["hotline.batch.id"] != first {
				return failf("the first 3 records are in more than one batch")
			}
		}
		if records[3].attrs["hotline.batch.id"] == first || records[3].attrs["hotline.batch.index"] != int64(0) {
			return failf("the 4th record is in batch %v at %v", records[3].attrs["hotline.batch.id"], records[3].attrs["hotline.batch.index"])
		}
		return nil
	})
	sc.Step(`^the exporter got 1 batch record, with hotline.batch.records 1$`, func() error {
		batches, err := w.waitBatches(1)
		if err != nil {
			return err
		}
		return has(batches[0].attrs, "hotline.batch.records", int64(1))
	})
	sc.Step(`^no integration sent a batch record for the windows without calls$`, func() error {
		pause(200 * time.Millisecond)
		if batches := w.receivers[0].ledger(); len(batches) != 1 {
			return failf("the exporter got %d batch records", len(batches))
		}
		return nil
	})
	sc.Step(`^the two batch records have hotline.batch.sequence 0 and 1, and the second's previous is the root of the first$`, func() error {
		batches, err := w.waitBatches(2)
		if err != nil {
			return err
		}
		return checkChain(batches[:2])
	})
	sc.Step(`^the checkout batch records have hotline.batch.sequence 0, 1 and 2, with one hotline.batch.instance$`, func() error {
		batches, err := w.waitBatches(3)
		if err != nil {
			return err
		}
		for i, b := range batches {
			if err := has(b.attrs, "hotline.batch.sequence", int64(i)); err != nil {
				return err
			}
			if b.attrs["hotline.batch.instance"] != batches[0].attrs["hotline.batch.instance"] {
				return failf("the batches have more than one instance")
			}
		}
		return nil
	})
	sc.Step(`^batch 0 has no hotline.batch.previous$`, func() error {
		batches, err := w.waitBatches(1)
		if err != nil {
			return err
		}
		return hasNo(batches[0].attrs, "hotline.batch.previous")
	})
	sc.Step(`^each later batch's hotline.batch.previous is the root of the batch before$`, func() error {
		batches, err := w.waitBatches(3)
		if err != nil {
			return err
		}
		return checkChain(batches)
	})
	sc.Step(`^the integrations? (\S+)(?: and (\S+))?$`, func(first, second string) error {
		w.integrations = nil
		for _, id := range []string{first, second} {
			if id == "" {
				continue
			}
			line, err := w.shopAPI("", "")
			if err != nil {
				return err
			}
			w.addIntegration(integration(id, line))
		}
		w.appID = first
		return nil
	})
	sc.Step(`^the app sends GET /orders as (\S+)(?: and GET /refunds as (\S+))?$`, func(first, second string) error {
		w.appID = first
		if err := w.shopCall("GET", "/orders", true, "", "", ""); err != nil {
			return err
		}
		if second == "" {
			return nil
		}
		w.appID = second
		return w.shopCall("GET", "/refunds", true, "", "", "")
	})
	sc.Step(`^the app sends GET /refunds as (\S+)$`, func(id string) error {
		w.appID = id
		return w.shopCall("GET", "/refunds", true, "", "", "")
	})
	sc.Step(`^there is one batch record with hotline.batch.integration checkout and one with refunds$`, func() error {
		batches, err := w.waitBatches(2)
		if err != nil {
			return err
		}
		chains := map[any]int{}
		for _, b := range batches {
			chains[b.attrs["hotline.batch.integration"]]++
		}
		if chains["checkout"] != 1 || chains["refunds"] != 1 || len(chains) != 2 {
			return failf("the batch records are of %v", chains)
		}
		return nil
	})
	sc.Step(`^each holds only its own integration's record$`, func() error {
		batches, err := w.waitBatches(2)
		if err != nil {
			return err
		}
		records, err := w.waitRecords(2)
		if err != nil {
			return err
		}
		for _, b := range batches {
			mine := 0
			for _, r := range records {
				if r.attrs["hotline.batch.id"] == b.attrs["hotline.batch.id"] {
					mine++
					if r.attrs["hotline.integration.id"] != b.attrs["hotline.batch.integration"] {
						return failf("the %v batch holds a record of %v", b.attrs["hotline.batch.integration"], r.attrs["hotline.integration.id"])
					}
				}
			}
			if mine != 1 || b.attrs["hotline.batch.records"] != int64(1) {
				return failf("the %v batch holds %d records", b.attrs["hotline.batch.integration"], mine)
			}
		}
		return nil
	})
	sc.Step(`^each chain starts at sequence 0$`, func() error {
		batches, err := w.waitBatches(2)
		if err != nil {
			return err
		}
		for _, b := range batches {
			if err := has(b.attrs, "hotline.batch.sequence", int64(0)); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the minimal record is in a batch with hotline.batch.integration not-passed$`, func() error {
		batches, err := w.waitBatches(1)
		if err != nil {
			return err
		}
		records, err := w.waitRecords(1)
		if err != nil {
			return err
		}
		if records[0].attrs["hotline.batch.id"] != batches[0].attrs["hotline.batch.id"] {
			return failf("the record is not in the batch")
		}
		return has(batches[0].attrs, "hotline.batch.integration", "not-passed")
	})
	sc.Step(`^refunds is added to the config and reloaded$`, func() error {
		line, err := w.shopAPI("", "")
		if err != nil {
			return err
		}
		w.addIntegration(integration("refunds", line))
		return w.reloadConfig()
	})
	sc.Step(`^refunds is removed from the config and reloaded$`, func() error {
		w.integrations = slices.DeleteFunc(w.integrations, func(yaml string) bool { return strings.Contains(yaml, "id: refunds\n") })
		return w.reloadConfig()
	})
	sc.Step(`^the config is changed and reloaded$`, func() error {
		w.topExtra += "# changed\n"
		return w.reloadConfig()
	})
	sc.Step(`^the (refunds|checkout) chain starts at sequence 0$`, func(chain string) error {
		batches, err := w.chain(chain, 1)
		if err != nil {
			return err
		}
		return has(batches[0].attrs, "hotline.batch.sequence", int64(0))
	})
	sc.Step(`^the checkout chain goes on unbroken$`, func() error {
		batches, err := w.chain("checkout", 2)
		if err != nil {
			return err
		}
		return checkChain(batches)
	})
	sc.Step(`^the refunds chain ends with a batch with hotline.batch.last true$`, func() error {
		batches, err := w.chain("refunds", 1)
		if err != nil {
			return err
		}
		return has(batches[len(batches)-1].attrs, "hotline.batch.last", true)
	})
	sc.Step(`^the refunds chain ends with an empty last batch of the config of the batch before it$`, func() error {
		batches, err := w.chain("refunds", 2)
		if err != nil {
			return err
		}
		before, last := batches[len(batches)-2], batches[len(batches)-1]
		if err := has(last.attrs, "hotline.batch.last", true); err != nil {
			return err
		}
		if err := has(last.attrs, "hotline.batch.records", int64(0)); err != nil {
			return err
		}
		return has(last.attrs, "hotline.batch.config", before.attrs["hotline.batch.config"])
	})
	sc.Step(`^the batch ids, sorted as text, are in sequence order$`, func() error {
		batches, err := w.waitBatches(3)
		if err != nil {
			return err
		}
		ids := make([]string, len(batches))
		for i, b := range batches {
			ids[i] = sprint(b.attrs["hotline.batch.id"])
		}
		if !slices.IsSorted(ids) {
			return failf("the batch ids are not in order: %v", ids)
		}
		return nil
	})
	sc.Step(`^no ledger section in the config$`, func() error { return nil })
	sc.Step(`^hotline-proxy starts$`, func() error { return w.start() })
	sc.Step(`^its batch settings are every 10 seconds and at most 65,536 records$`, func() error {
		logs := w.logs.String()
		if !strings.Contains(logs, "every=10s") || !strings.Contains(logs, "max_records=65536") {
			return failf("the proxy did not log its batch settings: %s", logs)
		}
		return nil
	})
	sc.Step(`^the batch record has hotline.batch.version, the proxy's version$`, func() error {
		batches, err := w.waitBatches(1)
		if err != nil {
			return err
		}
		return has(batches[0].attrs, "hotline.batch.version", "1.0.0-test")
	})
	sc.Step(`^hotline.batch.config is the SHA-256 of the config and its specs as loaded, which hotline.config.version is cut from$`, func() error {
		batches, err := w.waitBatches(1)
		if err != nil {
			return err
		}
		records, err := w.waitRecords(1)
		if err != nil {
			return err
		}
		_, short, _ := strings.Cut(sprint(records[0].attrs["hotline.config.version"]), "+")
		digest := sprint(batches[0].attrs["hotline.batch.config"])
		if len(digest) != len("sha256:")+64 || !strings.HasPrefix(digest, "sha256:"+short) {
			return failf("hotline.batch.config is %s, and the records' config version %s", digest, short)
		}
		return nil
	})
	sc.Step(`^the OTLP exporter's Authorization header is read from a file$`, func() error {
		file := w.writeFile("keys/otlp-token", "Bearer first-token\n")
		w.exporterExtra = []string{"      headers:\n        Authorization: { file: " + file + " }\n"}
		return nil
	})
	sc.Step(`^the file's token changes and the proxy restarts$`, func() error {
		w.settle()
		batches := w.receivers[0].ledger()
		if len(batches) == 0 {
			return failf("the first run sent no batch record")
		}
		before = batches[0].attrs
		w.writeFile("keys/otlp-token", "Bearer second-token\n")
		return w.start()
	})
	sc.Step(`^hotline.batch.config is the same as before$`, func() error {
		w.settle()
		batches := w.receivers[0].ledger()
		last := batches[len(batches)-1].attrs
		if last["hotline.batch.instance"] == before["hotline.batch.instance"] {
			return failf("the proxy did not restart")
		}
		return has(last, "hotline.batch.config", before["hotline.batch.config"])
	})
	sc.Step(`^the batch after the reload has a different hotline.batch.config$`, func() error {
		batches, err := w.waitBatches(2)
		if err != nil {
			return err
		}
		if batches[0].attrs["hotline.batch.config"] == batches[1].attrs["hotline.batch.config"] {
			return failf("both batches have %v", batches[0].attrs["hotline.batch.config"])
		}
		return nil
	})
	sc.Step(`^it still follows the chain of the batches before$`, func() error {
		batches, err := w.waitBatches(2)
		if err != nil {
			return err
		}
		return checkChain(batches[:2])
	})
	sc.Step(`^the vendor got no header it would not have got without the proxy$`, func() error {
		for _, c := range w.vendor("api.shop.test").received() {
			for name := range c.header {
				if strings.Contains(strings.ToLower(name), "hotline") || strings.Contains(strings.ToLower(name), "signature") {
					return failf("the vendor got the header %s", name)
				}
			}
		}
		return nil
	})
	sc.Step(`^the app got no header it would not have got without the proxy$`, func() error {
		if w.resp == nil {
			return failf("the app got no response")
		}
		for name := range w.resp.Header {
			if strings.Contains(strings.ToLower(name), "hotline") || strings.Contains(strings.ToLower(name), "signature") {
				return failf("the app got the header %s", name)
			}
		}
		return nil
	})
	sc.Step(`^hotline-proxy is stopped and started again$`, func() error {
		w.settle()
		return w.start()
	})
	sc.Step(`^hotline-proxy is stopped$`, func() error {
		w.settle()
		return nil
	})
	sc.Step(`^the two runs sign with different hotline.batch.key$`, func() error {
		runs, err := w.runBatches(2)
		if err != nil {
			return err
		}
		if runs[0][0].attrs["hotline.batch.key"] == runs[1][0].attrs["hotline.batch.key"] {
			return failf("both runs sign with %v", runs[0][0].attrs["hotline.batch.key"])
		}
		return nil
	})
	sc.Step(`^no file the proxy wrote holds a private key$`, func() error {
		return filepath.WalkDir(w.dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path) //nolint:gosec // the scenario's own folder
			if err != nil {
				return err
			}
			if strings.Contains(string(data), "PRIVATE KEY") || strings.Contains(string(data), "ed25519-priv") {
				return failf("%s holds a private key", path)
			}
			return nil
		})
	})
	sc.Step(`^the batches after it have a new hotline.batch.key$`, func() error {
		batches, err := w.waitBatches(2)
		if err != nil {
			return err
		}
		if batches[0].attrs["hotline.batch.key"] == batches[1].attrs["hotline.batch.key"] {
			return failf("the key did not change")
		}
		return nil
	})
	sc.Step(`^the chain goes on unbroken across the change$`, func() error {
		batches, err := w.waitBatches(2)
		if err != nil {
			return err
		}
		for _, b := range batches {
			if err := checkBatchSignature(b); err != nil {
				return err
			}
		}
		return checkChain(batches)
	})
	sc.Step(`^no ledger instance in the config$`, func() error { return nil })
	sc.Step(`^the two runs' hotline.batch.instance are different$`, func() error {
		runs, err := w.runBatches(2)
		if err != nil {
			return err
		}
		if runs[0][0].attrs["hotline.batch.instance"] == runs[1][0].attrs["hotline.batch.instance"] {
			return failf("both runs have the instance %v", runs[0][0].attrs["hotline.batch.instance"])
		}
		return nil
	})
	sc.Step(`^each is that run's service.instance.id$`, func() error {
		runs, err := w.runBatches(2)
		if err != nil {
			return err
		}
		for _, run := range runs {
			for _, b := range run {
				if b.attrs["hotline.batch.instance"] != b.resource["service.instance.id"] {
					return failf("the instance %v is not the run's %v", b.attrs["hotline.batch.instance"], b.resource["service.instance.id"])
				}
			}
		}
		return nil
	})
	sc.Step(`^the batches have no hotline.batch.run$`, func() error {
		w.settle()
		for _, b := range w.receivers[0].ledger() {
			if err := hasNo(b.attrs, "hotline.batch.run"); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the config has ledger instance \{ env: POD_NAME \} and POD_NAME is (\S+)$`, func(name string) error {
		if w.env == nil {
			w.env = map[string]string{}
		}
		w.env["POD_NAME"] = name
		w.topExtra += "ledger:\n  instance: { env: POD_NAME }\n"
		return nil
	})
	sc.Step(`^both runs' batches have hotline.batch.instance (\S+)$`, func(name string) error {
		w.settle()
		for _, b := range w.receivers[0].ledger() {
			if err := has(b.attrs, "hotline.batch.instance", name); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^their hotline.batch.run are different$`, func() error {
		runs, err := w.runBatches(2)
		if err != nil {
			return err
		}
		if runs[0][0].attrs["hotline.batch.run"] == nil || runs[0][0].attrs["hotline.batch.run"] == runs[1][0].attrs["hotline.batch.run"] {
			return failf("the runs have the run ids %v and %v", runs[0][0].attrs["hotline.batch.run"], runs[1][0].attrs["hotline.batch.run"])
		}
		return nil
	})
	sc.Step(`^each run's sequence starts at 0$`, func() error {
		runs, err := w.runBatches(2)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if err := has(run[0].attrs, "hotline.batch.sequence", int64(0)); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the last batch record has hotline.batch.last true and covers that call$`, func() error {
		w.settle()
		batches := w.receivers[0].ledger()
		records := w.receivers[0].got()
		if len(batches) == 0 || len(records) == 0 {
			return failf("the exporter got %d batch records and %d records", len(batches), len(records))
		}
		last := batches[len(batches)-1]
		if err := has(last.attrs, "hotline.batch.last", true); err != nil {
			return err
		}
		return has(records[len(records)-1].attrs, "hotline.batch.id", last.attrs["hotline.batch.id"])
	})
	sc.Step(`^no other batch has hotline.batch.last$`, func() error {
		batches := w.receivers[0].ledger()
		for _, b := range batches[:len(batches)-1] {
			if err := hasNo(b.attrs, "hotline.batch.last"); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the last batch record has hotline.batch.records 0 and hotline.batch.last true$`, func() error {
		batches := w.receivers[0].ledger()
		if len(batches) < 2 {
			return failf("the exporter got %d batch records", len(batches))
		}
		last := batches[len(batches)-1].attrs
		if err := has(last, "hotline.batch.records", int64(0)); err != nil {
			return err
		}
		return has(last, "hotline.batch.last", true)
	})
	sc.Step(`^hotline-proxy is killed$`, func() error {
		// What a killed proxy leaves is what it sent while it ran: the
		// batch records so far, and no last one. The proxy itself stops
		// when the scenario ends.
		batches, err := w.waitBatches(1)
		killed = batches
		return err
	})
	sc.Step(`^no batch record has hotline.batch.last$`, func() error {
		for _, b := range killed {
			if err := hasNo(b.attrs, "hotline.batch.last"); err != nil {
				return err
			}
		}
		return nil
	})
	exporterLedgerSteps(sc, w)
}

// ordersTimes sends GET /orders n times, one after another.
func (w *world) ordersTimes(n int) error {
	before := w.callsMade()
	for range n {
		if err := w.shopCall("GET", "/orders", true, "", "", ""); err != nil {
			return err
		}
	}
	w.waitCalls(before + n)
	return nil
}

// callsMade counts the call records the first receiver got or refused.
func (w *world) callsMade() int {
	if len(w.receivers) == 0 {
		return 0
	}
	return w.receivers[0].callsMade()
}

// waitCalls waits a little until the first receiver has seen n call
// records. A record is made after the response, in the background, so time
// passed on the proxy's clock right after a call could otherwise close its
// batch window before the record joined it.
func (w *world) waitCalls(n int) {
	if len(w.receivers) == 0 {
		return
	}
	eventually(waitLimit, func() bool { return w.receivers[0].callsMade() >= n })
}

// reloadConfig writes the config again and has the proxy load it.
func (w *world) reloadConfig() error {
	if err := w.start(); err != nil {
		return err
	}
	w.replaceFile("hotline-proxy.yaml", w.config())
	w.proxy.Reload()
	return nil
}

// waitBatches waits, while the proxy runs, until the first exporter has
// got at least n batch records, and gives them in sequence order per chain.
func (w *world) waitBatches(n int) ([]receivedRecord, error) {
	var batches []receivedRecord
	if !eventually(waitLimit, func() bool { batches = w.receivers[0].ledger(); return len(batches) >= n }) {
		return nil, failf("the exporter got %d batch records, want %d", len(batches), n)
	}
	return batches, nil
}

// waitRecords waits, while the proxy runs, until the first exporter has
// got at least n call records.
func (w *world) waitRecords(n int) ([]receivedRecord, error) {
	var records []receivedRecord
	if !eventually(waitLimit, func() bool { records = w.receivers[0].got(); return len(records) >= n }) {
		return nil, failf("the exporter got %d records, want %d", len(records), n)
	}
	return records, nil
}

// chain is one integration's batch records, at least n of them.
func (w *world) chain(name string, n int) ([]receivedRecord, error) {
	var mine []receivedRecord
	found := eventually(waitLimit, func() bool {
		mine = nil
		for _, b := range w.receivers[0].ledger() {
			if b.attrs["hotline.batch.integration"] == name {
				mine = append(mine, b)
			}
		}
		return len(mine) >= n
	})
	if !found {
		return nil, failf("the %s chain has %d batch records, want %d", name, len(mine), n)
	}
	return mine, nil
}

// runBatches are the batch records of each run, once the proxy stopped.
func (w *world) runBatches(n int) ([][]receivedRecord, error) {
	w.settle()
	byRun := map[string][]receivedRecord{}
	var order []string
	for _, b := range w.receivers[0].ledger() {
		run := sprint(b.resource["service.instance.id"])
		if _, seen := byRun[run]; !seen {
			order = append(order, run)
		}
		byRun[run] = append(byRun[run], b)
	}
	if len(order) != n {
		return nil, failf("the batch records come from %d runs, want %d", len(order), n)
	}
	runs := make([][]receivedRecord, n)
	for i, run := range order {
		runs[i] = byRun[run]
	}
	return runs, nil
}

// batchOf is the batch record a record names.
func (w *world) batchOf(r receivedRecord) (receivedRecord, error) {
	w.settle()
	for _, b := range w.receivers[0].ledger() {
		if b.attrs["hotline.batch.id"] == r.attrs["hotline.batch.id"] {
			return b, nil
		}
	}
	return receivedRecord{}, failf("no batch record has the id %v", r.attrs["hotline.batch.id"])
}

// bodyHashIs checks that the last record's body hash of one side is the
// body hash of body.
func (w *world) bodyHashIs(side string, body []byte) error {
	records := w.records()
	if len(records) == 0 {
		return failf("no record arrived")
	}
	r := records[len(records)-1]
	return has(r.attrs, "hotline."+side+".body.hash", bodyHash(side, r.traceID, r.spanID, body))
}

// ledgerField says whether a record attribute is the ledger's own: its
// hashes and its place in a batch, which every record has.
func ledgerField(name string) bool {
	switch name {
	case "hotline.record.hash", "hotline.trace.hash", "hotline.batch.id", "hotline.batch.index",
		"hotline.request.body.hash", "hotline.response.body.hash":
		return true
	}
	return false
}

// hashBytes are the bytes a ledger hash covers, built the way a checker in
// any language would: each piece is its length as 8 bytes, then its bytes;
// each entry is its name as a piece, then its value.
type hashBytes struct{ buf []byte }

func (b *hashBytes) piece(p []byte) {
	b.buf = binary.BigEndian.AppendUint64(b.buf, uint64(len(p)))
	b.buf = append(b.buf, p...)
}

func (b *hashBytes) entry(name string, v any) {
	b.piece([]byte(name))
	b.value(v)
}

// value is a type letter, then: text as a piece; a whole or decimal number
// as 8 bytes; true or false as one byte; a list or map as its count in 8
// bytes, then each item, a map's in name order.
func (b *hashBytes) value(v any) {
	switch x := v.(type) {
	case string:
		b.buf = append(b.buf, 's')
		b.piece([]byte(x))
	case int64:
		b.buf = append(b.buf, 'i')
		b.buf = binary.BigEndian.AppendUint64(b.buf, uint64(x)) //nolint:gosec // the bits as they are
	case float64:
		b.buf = append(b.buf, 'f')
		b.buf = binary.BigEndian.AppendUint64(b.buf, math.Float64bits(x))
	case bool:
		b.buf = append(b.buf, 'b', map[bool]byte{false: 0, true: 1}[x])
	case []any:
		b.buf = append(b.buf, 'l')
		b.buf = binary.BigEndian.AppendUint64(b.buf, uint64(len(x)))
		for _, item := range x {
			b.value(item)
		}
	case map[string]any:
		b.buf = append(b.buf, 'm')
		b.buf = binary.BigEndian.AppendUint64(b.buf, uint64(len(x)))
		for _, name := range slices.Sorted(maps.Keys(x)) {
			b.entry(name, x[name])
		}
	default:
		panic(fmt.Sprintf("a value of type %T cannot be hashed", v))
	}
}

// sum is the SHA-256 of the bytes, as the ledger writes it.
func (b *hashBytes) sum() string {
	s := sha256.Sum256(b.buf)
	return "sha256:" + hex.EncodeToString(s[:])
}

// recordHash rebuilds a record's hash as docs/proxy-ledger.md describes
// it: the word hotline.record.v2, every attribute but the hash in name
// order, then every other field of the record.
func recordHash(r receivedRecord) string {
	var b hashBytes
	b.piece([]byte("hotline.record.v2"))
	for _, name := range slices.Sorted(maps.Keys(r.attrs)) {
		if name != "hotline.record.hash" {
			b.entry(name, r.attrs[name])
		}
	}
	b.recordFields(r)
	return b.sum()
}

// recordFields are the fields of a record besides its attributes, in the
// order a record's hash and a batch's signature cover them: every field the
// proxy sends, by its OTLP name, times in microseconds.
func (b *hashBytes) recordFields(r receivedRecord) {
	resource := r.resource
	if resource == nil {
		resource = map[string]any{}
	}
	b.entry("log.record.timestamp", int64(r.timestamp/1000))    //nolint:gosec // microseconds since 1970 fit
	b.entry("log.record.observed_time", int64(r.observed/1000)) //nolint:gosec // as above
	b.entry("log.record.trace_id", r.traceID)
	b.entry("log.record.span_id", r.spanID)
	b.entry("log.record.flags", int64(r.flags))
	b.entry("log.record.severity_number", int64(r.severityNumber))
	b.entry("log.record.severity_text", r.severityText)
	b.entry("log.record.event_name", r.eventName)
	b.entry("log.record.body", r.body)
	b.entry("log.record.resource", resource)
}

// coveredFields are the LogRecord fields a record's hash covers, by their
// number in OTLP: time, level number and text, body, attributes, flags,
// trace id, span id, observed time and event name (Peter, 2026-11-25).
func coveredFields() []int { return []int{1, 2, 3, 5, 6, 8, 9, 10, 11, 12} }

// severityNumber is the number of a level the proxy writes, by its text.
func severityNumber(text string) int32 {
	return map[string]int32{"INFO": 9, "WARN": 13, "ERROR": 17}[text]
}

func checkRecordHash(r receivedRecord) error {
	if got, want := r.attrs["hotline.record.hash"], recordHash(r); got != want {
		return failf("the record's hash %v does not check out, it rebuilds as %s", got, want)
	}
	return nil
}

// checkTraceHash rebuilds a trace's hash from its spans and checks it
// against the record's hotline.trace.hash.
func checkTraceHash(t callTrace) error {
	spans := slices.Clone(t.spans)
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start/1000 != spans[j].start/1000 {
			return spans[i].start < spans[j].start
		}
		return spans[i].spanID < spans[j].spanID
	})
	var b hashBytes
	b.piece([]byte("hotline.trace.v2"))
	b.entry("trace.trace_id", t.record.traceID)
	for _, s := range spans {
		b.entry("span.span_id", s.spanID)
		b.entry("span.parent_span_id", s.parent)
		b.entry("span.name", s.name)
		b.entry("span.kind", s.kind)
		b.entry("span.start_time", s.start/1000)
		b.entry("span.end_time", s.end/1000)
		b.entry("span.status", s.status)
		b.entry("span.flags", int64(s.flags))
		b.entry("span.link_count", int64(len(s.links)))
		for i, link := range s.links {
			traceID, spanID, _ := strings.Cut(link, "/")
			b.entry("link.trace_id", traceID)
			b.entry("link.span_id", spanID)
			b.entry("link.flags", int64(s.linkFlags[i]))
		}
		b.entry("span.attribute_count", int64(len(s.attrs)))
		for _, name := range slices.Sorted(maps.Keys(s.attrs)) {
			b.entry(name, s.attrs[name])
		}
	}
	if got, want := t.record.attrs["hotline.trace.hash"], b.sum(); got != want {
		return failf("the trace's hash %v does not check out, it rebuilds as %s", got, want)
	}
	return nil
}

// bodyHash is a body's hash: SHA-256 of the word hotline.body.v1 and the
// side as pieces, the trace id's 16 bytes, the span id's 8, then the body.
func bodyHash(side, traceID, spanID string, body []byte) string {
	var b hashBytes
	b.piece([]byte("hotline.body.v1"))
	b.piece([]byte(side))
	t, _ := hex.DecodeString(traceID)
	s, _ := hex.DecodeString(spanID)
	b.buf = append(append(append(b.buf, t...), s...), body...)
	return b.sum()
}

// rootOf is the root of a tree of record hashes, as in RFC 6962.
func rootOf(hashes []string) string {
	leaves := make([][]byte, len(hashes))
	for i, h := range hashes {
		leaves[i], _ = hex.DecodeString(strings.TrimPrefix(h, "sha256:"))
	}
	root := treeRoot(leaves)
	return "sha256:" + hex.EncodeToString(root)
}

func treeRoot(leaves [][]byte) []byte {
	switch len(leaves) {
	case 0:
		s := sha256.Sum256(nil)
		return s[:]
	case 1:
		s := sha256.Sum256(append([]byte{0}, leaves[0]...))
		return s[:]
	}
	k := 1
	for k*2 < len(leaves) {
		k *= 2
	}
	s := sha256.Sum256(append(append([]byte{1}, treeRoot(leaves[:k])...), treeRoot(leaves[k:])...))
	return s[:]
}

// checkRoot rebuilds a batch's root from the records that name it, and
// the dropped hashes by their index.
func checkRoot(batch receivedRecord, records []receivedRecord, dropped map[int64]string) error {
	count, _ := batch.attrs["hotline.batch.records"].(int64)
	leaves := make([]string, count)
	for _, r := range records {
		if r.attrs["hotline.batch.id"] == batch.attrs["hotline.batch.id"] {
			leaves[r.attrs["hotline.batch.index"].(int64)] = sprint(r.attrs["hotline.record.hash"])
		}
	}
	for index, hash := range dropped {
		leaves[index] = hash
	}
	if got := rootOf(leaves); got != batch.attrs["hotline.batch.root"] {
		return failf("the root rebuilds as %s, the batch has %v", got, batch.attrs["hotline.batch.root"])
	}
	return nil
}

// checkBatchSignature checks a batch record's signature with the key it
// names.
func checkBatchSignature(r receivedRecord) error {
	return checkSigned("hotline.batch.v2", "hotline.batch.signature", r)
}

// checkSigned checks a batch or drop record's signature with the key it
// names: SHA-256 of the format word, every attribute but the signature in
// name order, and every other field of the record as a record's hash
// covers it, signed with Ed25519.
func checkSigned(format, signatureName string, r receivedRecord) error {
	digest := signedDigest(format, r)
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sprint(r.attrs["hotline.batch.key"]), "ed25519:"))
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sprint(r.attrs[signatureName]), "ed25519:"))
	if err != nil {
		return err
	}
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, digest[:], signature) {
		return failf("the %s does not check out with its key", signatureName)
	}
	return nil
}

// signedDigest is what a batch or drop record's signature covers: every
// attribute but its own signature.
func signedDigest(format string, r receivedRecord) [sha256.Size]byte {
	signature := map[string]string{"hotline.batch.v2": "hotline.batch.signature", "hotline.dropped.v1": "hotline.dropped.signature"}[format]
	var b hashBytes
	b.piece([]byte(format))
	for _, name := range slices.Sorted(maps.Keys(r.attrs)) {
		if name != signature {
			b.entry(name, r.attrs[name])
		}
	}
	b.recordFields(r)
	return sha256.Sum256(b.buf)
}

// checkChain checks that batches are one chain: sequence 0 on, each naming
// the root of the one before.
func checkChain(batches []receivedRecord) error {
	for i, b := range batches {
		if err := has(b.attrs, "hotline.batch.sequence", int64(i)); err != nil {
			return err
		}
		if i == 0 {
			continue
		}
		if err := has(b.attrs, "hotline.batch.previous", batches[i-1].attrs["hotline.batch.root"]); err != nil {
			return err
		}
	}
	return nil
}
