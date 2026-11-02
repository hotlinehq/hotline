package compare_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-load/compare"
	"hotline-load/run"
)

// save writes one repeat the way the runner does.
func save(dir, runName string, repeat int, r run.Result) {
	folder := filepath.Join(dir, runName, "repeat-"+string(rune('0'+repeat)))
	Expect(os.MkdirAll(folder, 0o750)).To(Succeed())
	raw, err := json.Marshal(r)
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(filepath.Join(folder, "result.json"), raw, 0o600)).To(Succeed())
}

func result(label string, cpu, kb, p95 float64) run.Result {
	return run.Result{Shape: "large-json", Rate: 200, Label: label, CPUMicrosPerCall: cpu,
		AllocatedBytesPerCall: kb * 1024, ExtraP95: p95, Proxy: run.Latency{Calls: 4000}}
}

var _ = Describe("Comparing runs", func() {
	It("lists every repeat of every label side by side, nothing averaged", func() {
		dir := GinkgoT().TempDir()
		save(dir, "run-001", 1, result("main", 5800, 1980, 1.5))
		save(dir, "run-002", 1, result("branch", 2400, 700, 1.1))
		save(dir, "run-003", 1, result("main", 5900, 1990, 1.6))
		save(dir, "run-004", 1, result("branch", 2500, 720, 1.2))

		out, err := compare.Table(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring("large-json, 200 calls a second"))
		Expect(out).To(MatchRegexp(`main\s+1\s+5800\s+1980\.0\s+1\.50`))
		Expect(out).To(MatchRegexp(`main\s+2\s+5900\s+1990\.0\s+1\.60`))
		Expect(out).To(MatchRegexp(`branch\s+1\s+2400\s+700\.0\s+1\.10`))
		Expect(out).To(MatchRegexp(`branch\s+2\s+2500\s+720\.0\s+1\.20`))
	})

	It("gives the range of change of the second label against the first, from its best to its worst repeat", func() {
		dir := GinkgoT().TempDir()
		save(dir, "run-001", 1, result("main", 5800, 2000, 1.5))
		save(dir, "run-002", 1, result("branch", 2400, 800, 1.1))
		save(dir, "run-003", 1, result("main", 6000, 2000, 1.6))
		save(dir, "run-004", 1, result("branch", 3000, 1000, 1.2))

		out, err := compare.Table(dir)
		Expect(err).NotTo(HaveOccurred())
		// Memory: branch 800 to 1000 against main 2000: -60% to -50%.
		Expect(out).To(MatchRegexp(`memory per call.*-60%.*-50%`))
		// Processor: branch 2400 to 3000 against main 5800 to 6000.
		Expect(out).To(MatchRegexp(`processor time per call.*-60%.*-48%`))
	})

	It("keeps call shapes and rates apart", func() {
		dir := GinkgoT().TempDir()
		a := result("main", 1, 1, 1)
		b := result("main", 1, 1, 1)
		b.Rate = 500
		save(dir, "run-001", 1, a)
		save(dir, "run-002", 1, b)
		out, err := compare.Table(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring("200 calls a second"))
		Expect(out).To(ContainSubstring("500 calls a second"))
	})

	It("says plainly when the folder holds no results", func() {
		_, err := compare.Table(GinkgoT().TempDir())
		Expect(err).To(MatchError(ContainSubstring("no result.json")))
	})
})
