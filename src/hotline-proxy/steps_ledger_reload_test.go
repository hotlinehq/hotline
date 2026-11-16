package proxy_test

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"
)

// ledgerReloadSteps are the steps of the ledger scenarios where a reload
// comes while a call is in flight: the vendor holds its answer to one
// call, the config is reloaded, then the vendor answers.
func ledgerReloadSteps(sc *godog.ScenarioContext, w *world) {
	// arrived closes when the held call reaches the vendor, release lets
	// the vendor answer it, and sent gives the app's side of the call
	// once it ended.
	var arrived, release chan struct{}
	var sent chan error

	sc.Step(`^the vendor holds its answer to the next call$`, func() error {
		if err := w.start(); err != nil {
			return err
		}
		// Calls already made join their batches first.
		w.waitCalls(w.sent)
		arrived, release = make(chan struct{}), make(chan struct{})
		var once sync.Once
		held, freed := arrived, release
		w.vendor("api.shop.test").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			first := false
			once.Do(func() { first = true })
			if first {
				close(held)
				<-freed
			}
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(`{"ok":true}`))
		})
		return nil
	})
	sc.Step(`^the app sends GET /(orders|refunds) as (\S+), which waits for the vendor$`, func(path, id string) error {
		if arrived == nil {
			return failf("no vendor holds its answer")
		}
		w.appID = id
		sent = make(chan error, 1)
		go func() { sent <- w.shopCall("GET", "/"+path, true, "", "", "") }()
		select {
		case <-arrived:
			return nil
		case err := <-sent:
			return failf("the call ended before it reached the vendor: %v", err)
		case <-time.After(5 * time.Second):
			return failf("the call never reached the vendor")
		}
	})
	sc.Step(`^the vendor answers the call it held$`, func() error {
		close(release)
		select {
		case err := <-sent:
			return err
		case <-time.After(5 * time.Second):
			return failf("the app got no answer to the held call")
		}
	})
	sc.Step(`^the two records are in two batches of the checkout chain, one after the other$`, func() error {
		batches, err := w.chain("checkout", 2)
		if err != nil {
			return err
		}
		records, err := w.waitRecords(2)
		if err != nil {
			return err
		}
		if len(batches) != 2 {
			return failf("the checkout chain has %d batches, want 2", len(batches))
		}
		for _, b := range batches {
			if err := has(b.attrs, "hotline.batch.records", int64(1)); err != nil {
				return err
			}
		}
		if records[0].attrs["hotline.batch.id"] == records[1].attrs["hotline.batch.id"] {
			return failf("both records are in batch %v", records[0].attrs["hotline.batch.id"])
		}
		return checkChain(batches)
	})
	sc.Step(`^each batch's hotline.batch.config is the config of the record in it$`, func() error {
		batches, err := w.chain("checkout", 2)
		if err != nil {
			return err
		}
		records, err := w.waitRecords(2)
		if err != nil {
			return err
		}
		if batches[0].attrs["hotline.batch.config"] == batches[1].attrs["hotline.batch.config"] {
			return failf("both batches name the config %v", batches[0].attrs["hotline.batch.config"])
		}
		for _, r := range records {
			_, short, _ := strings.Cut(sprint(r.attrs["hotline.config.version"]), "+")
			b := batchOf(batches, r)
			if b == nil {
				return failf("the record is in no checkout batch")
			}
			if digest := sprint(b.attrs["hotline.batch.config"]); !strings.HasPrefix(digest, "sha256:"+short) {
				return failf("a record of config %s is in a batch of config %s", short, digest)
			}
		}
		return nil
	})
	sc.Step(`^the refunds chain goes on unbroken after its last batch$`, func() error {
		batches, err := w.chain("refunds", 2)
		if err != nil {
			return err
		}
		if err := has(batches[0].attrs, "hotline.batch.last", true); err != nil {
			return err
		}
		return checkChain(batches)
	})
	sc.Step(`^the held call's record is in the refunds chain, after the last batch$`, func() error {
		batches, err := w.chain("refunds", 2)
		if err != nil {
			return err
		}
		records, err := w.waitRecords(2)
		if err != nil {
			return err
		}
		held := records[len(records)-1]
		if err := has(held.attrs, "hotline.integration.id", "refunds"); err != nil {
			return err
		}
		b := batchOf(batches, held)
		if b == nil {
			return failf("the held call's record is in no refunds batch; it names batch %v", held.attrs["hotline.batch.id"])
		}
		if b == &batches[0] {
			return failf("the held call's record is in the last batch, made before it ended")
		}
		return nil
	})
}

// batchOf is the batch record, among batches, that a record names.
func batchOf(batches []receivedRecord, r receivedRecord) *receivedRecord {
	for i := range batches {
		if batches[i].attrs["hotline.batch.id"] == r.attrs["hotline.batch.id"] {
			return &batches[i]
		}
	}
	return nil
}
