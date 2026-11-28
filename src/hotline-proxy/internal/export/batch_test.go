package export_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
	"hotline-proxy/internal/vault"
)

// A signer that fails, which the proxy's own vault never hands out from a
// config it accepted.
var _ = Describe("Signing a batch record", func() {
	sut := exportersSUT{}

	It("lets go of a batch record its signer cannot sign, and says so", func() {
		sut.forFileExporter()
		sut.Batch("b-1", sut.SignerOfKeyType("dsa"))
		sut.Stop()
		Expect(sut.Logs()).To(ContainSubstring("could not sign a batch record, it is let go"))
		Expect(sut.FileText()).NotTo(ContainSubstring("hotline.ledger.batch"))
	})
})

var _ = Describe("Queueing a batch record", func() {
	sut := exportersSUT{}

	It("counts a batch record lost when even the places kept for batch records are taken", func() {
		// Nothing answers there, so nothing leaves the queue, which has
		// room for 1 item and 64 batch records.
		sut.forOTLPExporter(config.Exporter{
			Endpoint: "http://127.0.0.1:1", URL: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}, QueueSize: 1,
			LogInterval: time.Hour, TraceInterval: time.Hour,
		})
		signer := sut.SignerOfKeyType(vault.MLDSA44)
		for i := range 66 {
			sut.Batch(fmt.Sprintf("b-%d", i), signer)
		}
		// The stop waits for the signing, then gives up on the busy
		// collector.
		Expect(sut.StopWithin(time.Second)).To(Succeed())
		Expect(sut.Dropped("batch")).To(Equal(int64(1)))
	})
})

// exportersSUT is the proxy's exporters, built as the proxy builds them,
// with what they logged and the numbers they keep.
type exportersSUT struct {
	exporters *export.Exporters
	dir       string
	logs      *lockedLog
	reader    *sdkmetric.ManualReader
	stopped   bool
}

// forFileExporter is one file exporter writing to a new folder, with a
// megabyte for its file and its queue.
func (s *exportersSUT) forFileExporter() {
	s.dir = GinkgoT().TempDir()
	s.start(export.Setup{
		Files: []config.FileExporter{{Path: s.dir, MaxFileSize: 1 << 20, MaxFiles: 2, QueueMemory: 1 << 20}},
	})
}

// forOTLPExporter is one OTLP exporter as the config gives it, whose each
// send may take up to 2 seconds.
func (s *exportersSUT) forOTLPExporter(e config.Exporter) { s.forOTLPExporterWithin(e, 2*time.Second) }

// forOTLPExporterWithin is one OTLP exporter whose each send may take up
// to timeout.
func (s *exportersSUT) forOTLPExporterWithin(e config.Exporter, timeout time.Duration) {
	s.start(export.Setup{OTLP: []config.Exporter{e}, Timing: export.Timing{Timeout: timeout}})
}

// StartErrorWithFolderInPlaceOfTheLock is why a file exporter could not
// start in a folder where a folder stands in place of its lock file.
func (s *exportersSUT) StartErrorWithFolderInPlaceOfTheLock() error {
	s.prepare()
	s.dir = GinkgoT().TempDir()
	Expect(os.Mkdir(filepath.Join(s.dir, ".lock"), 0o750)).WithOffset(1).To(Succeed())
	exporters, err := export.New(s.withDefaults(export.Setup{
		Files: []config.FileExporter{{Path: s.dir, MaxFileSize: 1 << 20, MaxFiles: 2}},
	}))
	if err == nil {
		s.exporters = exporters
		DeferCleanup(s.Close)
	}
	return err
}

func (s *exportersSUT) start(setup export.Setup) {
	s.prepare()
	var err error
	s.exporters, err = export.New(s.withDefaults(setup))
	Expect(err).WithOffset(2).NotTo(HaveOccurred())
	DeferCleanup(s.Close)
}

func (s *exportersSUT) prepare() {
	s.logs, s.reader, s.stopped = &lockedLog{}, sdkmetric.NewManualReader(), false
}

func (s *exportersSUT) withDefaults(setup export.Setup) export.Setup {
	setup.Logger = slog.New(slog.NewTextHandler(s.logs, nil))
	setup.Meter = sdkmetric.NewMeterProvider(sdkmetric.WithReader(s.reader)).Meter("test")
	return setup
}

// Close stops the exporters, if the test did not already.
func (s *exportersSUT) Close() {
	if !s.stopped {
		_ = s.StopWithin(time.Second)
	}
}

// Call exports one finished call.
func (s *exportersSUT) Call(c export.Call) { s.exporters.Call(context.Background(), c) }

// Batch exports the record of the ledger batch id, signed by signer.
func (s *exportersSUT) Batch(id string, signer vault.Signer) {
	at := time.UnixMicro(1_700_000_000_000_000)
	s.exporters.Batch(export.Batch{ID: id, Event: "hotline.ledger.batch", At: at, Observed: at}, signer)
}

// SignerOfKeyType is a vault's signer of keys of type keyType.
func (*exportersSUT) SignerOfKeyType(keyType string) vault.Signer {
	at := time.UnixMicro(1_700_000_000_000_000)
	keys := vault.HourlyKeys{Type: keyType}
	return vault.New("hotline-proxy", time.Hour, keys, at).Signer(keys, at)
}

// Stop sends what waits and stops the exporters, failing the test if they
// could not.
func (s *exportersSUT) Stop() {
	s.stopped = true
	Expect(s.exporters.Shutdown(context.Background())).WithOffset(1).To(Succeed())
}

// StopWithin stops the exporters, giving them limit to send what waits.
func (s *exportersSUT) StopWithin(limit time.Duration) error {
	s.stopped = true
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	return s.exporters.Shutdown(ctx)
}

// StopsAtOnceWithNoTimeLeft stops the exporters with no time left to send,
// and says whether the stop came back within a second.
func (s *exportersSUT) StopsAtOnceWithNoTimeLeft() bool {
	s.stopped = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		// Its error says the time ran out, which is what this stop is for.
		_ = s.exporters.Shutdown(ctx)
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(time.Second):
		return false
	}
}

// Logs is what the exporters logged so far.
func (s *exportersSUT) Logs() string { return s.logs.String() }

// FileText is what the file exporter wrote to its current file.
func (s *exportersSUT) FileText() string {
	b, err := os.ReadFile(filepath.Join(s.dir, "calls.jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	return string(b)
}

// Dropped is how many items of signal the exporters counted as lost.
func (s *exportersSUT) Dropped(signal string) int64 {
	var got metricdata.ResourceMetrics
	Expect(s.reader.Collect(context.Background(), &got)).WithOffset(1).To(Succeed())
	lost := int64(0)
	for _, sm := range got.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if m.Name != "hotline.exporter.dropped" || !ok {
				continue
			}
			for _, p := range sum.DataPoints {
				if v, _ := p.Attributes.Value(attribute.Key("hotline.signal")); v.AsString() == signal {
					lost += p.Value
				}
			}
		}
	}
	return lost
}

// Gauge is the last value the exporters gave the gauge name for signal.
func (s *exportersSUT) Gauge(name, signal string) float64 {
	var got metricdata.ResourceMetrics
	Expect(s.reader.Collect(context.Background(), &got)).WithOffset(1).To(Succeed())
	for _, sm := range got.ScopeMetrics {
		for _, m := range sm.Metrics {
			gauge, ok := m.Data.(metricdata.Gauge[float64])
			if m.Name != name || !ok {
				continue
			}
			for _, p := range gauge.DataPoints {
				if v, _ := p.Attributes.Value(attribute.Key("hotline.signal")); v.AsString() == signal {
					return p.Value
				}
			}
		}
	}
	return 0
}
