package export_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
	"hotline-proxy/internal/vault"
)

// A signer that fails, which the proxy's own vault never hands out from a
// config it accepted.
var _ = Describe("Signing a batch record", func() {
	It("lets go of a batch record its signer cannot sign, and says so", func() {
		dir := GinkgoT().TempDir()
		var logs bytes.Buffer
		exporters, err := export.New(export.Setup{
			Files:  []config.FileExporter{{Path: dir, MaxFileSize: 1 << 20, MaxFiles: 2, QueueMemory: 1 << 20}},
			Logger: slog.New(slog.NewTextHandler(&logs, nil)),
			Meter:  noop.NewMeterProvider().Meter("test"),
		})
		Expect(err).NotTo(HaveOccurred())
		at := time.UnixMicro(1_700_000_000_000_000)
		failing := vault.New("hotline-proxy", time.Hour, vault.HourlyKeys{Type: "dsa"}, at).Signer(vault.HourlyKeys{Type: "dsa"}, at)

		exporters.Batch(export.Batch{ID: "b-1", Event: "hotline.ledger.batch", At: at, Observed: at}, failing)
		Expect(exporters.Shutdown(context.Background())).To(Succeed())

		Expect(logs.String()).To(ContainSubstring("could not sign a batch record, it is let go"))
		lines, _ := os.ReadFile(filepath.Join(dir, "calls.jsonl")) //nolint:gosec // a folder the test made
		Expect(string(lines)).NotTo(ContainSubstring("hotline.ledger.batch"))
	})
})

var _ = Describe("Queueing a batch record", func() {
	It("counts a batch record lost when even the places kept for batch records are taken", func() {
		reader := sdkmetric.NewManualReader()
		exporters, err := export.New(export.Setup{
			// Nothing answers there, so nothing leaves the queue.
			OTLP: []config.Exporter{{
				Endpoint: "http://127.0.0.1:1", URL: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}, QueueSize: 1,
				LogInterval: time.Hour, TraceInterval: time.Hour,
			}},
			Logger: slog.New(slog.DiscardHandler),
			Meter:  sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test"),
		})
		Expect(err).NotTo(HaveOccurred())
		at := time.UnixMicro(1_700_000_000_000_000)
		signer := vault.New("hotline-proxy", time.Hour, vault.HourlyKeys{Type: vault.MLDSA44}, at).Signer(vault.HourlyKeys{Type: vault.MLDSA44}, at)
		// The queue has room for 1 item and 64 batch records.
		for i := range 66 {
			exporters.Batch(export.Batch{ID: fmt.Sprintf("b-%d", i), Event: "hotline.ledger.batch", At: at, Observed: at}, signer)
		}
		// The stop waits for the signing, then gives up on the busy
		// collector.
		stopping, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		Expect(exporters.Shutdown(stopping)).To(Succeed())

		var got metricdata.ResourceMetrics
		Expect(reader.Collect(context.Background(), &got)).To(Succeed())
		lost := int64(0)
		for _, sm := range got.ScopeMetrics {
			for _, m := range sm.Metrics {
				sum, ok := m.Data.(metricdata.Sum[int64])
				if m.Name != "hotline.exporter.dropped" || !ok {
					continue
				}
				for _, p := range sum.DataPoints {
					if signal, _ := p.Attributes.Value(attribute.Key("hotline.signal")); signal.AsString() == "batch" {
						lost += p.Value
					}
				}
			}
		}
		Expect(lost).To(Equal(int64(1)))
	})
})
