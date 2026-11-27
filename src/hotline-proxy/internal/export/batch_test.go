package export_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/metric/noop"

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
