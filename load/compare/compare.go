// Package compare sets the results of runs side by side: every repeat of
// every label, as it came, and the range of change between two labels.
// Nothing is averaged.
package compare

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"hotline-load/run"
)

var errNoResults = errors.New("no result.json found")

type group struct {
	shape  string
	rate   int
	labels []string
	byLabl map[string][]run.Result
}

// Table reads every result.json under dir and prints, for each call shape
// and rate, one line per repeat and label, then the range of change of the
// second label against the first.
func Table(dir string) (string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "result.json" {
			paths = append(paths, path)
		}
		return err
	})
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("%w under %s", errNoResults, dir)
	}
	sort.Strings(paths) // run-001 before run-002: the order the runs happened
	var groups []*group
	for _, path := range paths {
		raw, err := os.ReadFile(path) //nolint:gosec // a results folder the person named
		if err != nil {
			return "", err
		}
		var r run.Result
		if err := json.Unmarshal(raw, &r); err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		g := find(&groups, r)
		if _, seen := g.byLabl[r.Label]; !seen {
			g.labels = append(g.labels, r.Label)
		}
		g.byLabl[r.Label] = append(g.byLabl[r.Label], r)
	}
	var b strings.Builder
	for _, g := range groups {
		write(&b, g)
	}
	return b.String(), nil
}

func find(groups *[]*group, r run.Result) *group {
	for _, g := range *groups {
		if g.shape == r.Shape && g.rate == r.Rate {
			return g
		}
	}
	g := &group{shape: r.Shape, rate: r.Rate, byLabl: map[string][]run.Result{}}
	*groups = append(*groups, g)
	return g
}

func write(b *strings.Builder, g *group) {
	fmt.Fprintf(b, "\n%s, %d calls a second\n", g.shape, g.rate)
	b.WriteString("label      repeat  cpu us/call  allocated KB/call  extra p95 (ms)  extra p99 (ms)\n")
	for _, l := range g.labels {
		for i, r := range g.byLabl[l] {
			fmt.Fprintf(b, "%-10s %6d  %11.0f  %17.1f  %14.2f  %14.2f\n",
				l, i+1, r.CPUMicrosPerCall, r.AllocatedBytesPerCall/1024, r.ExtraP95, r.ExtraP99)
		}
	}
	if len(g.labels) < 2 {
		return
	}
	base, next := g.byLabl[g.labels[0]], g.byLabl[g.labels[1]]
	fmt.Fprintf(b, "change of %s against %s, from its best repeat to its worst:\n", g.labels[1], g.labels[0])
	line := func(name string, pick func(run.Result) float64) {
		lo, hi := rangeOf(base, pick), rangeOf(next, pick)
		fmt.Fprintf(b, "  %s: %+.0f%% to %+.0f%%\n", name, 100*(hi[0]-lo[1])/lo[1], 100*(hi[1]-lo[0])/lo[0])
	}
	line("memory per call", func(r run.Result) float64 { return r.AllocatedBytesPerCall })
	line("processor time per call", func(r run.Result) float64 { return r.CPUMicrosPerCall })
	line("extra delay of the slowest 5 in 100", func(r run.Result) float64 { return r.ExtraP95 })
}

// rangeOf is the lowest and highest value of the repeats.
func rangeOf(rs []run.Result, pick func(run.Result) float64) [2]float64 {
	lo, hi := pick(rs[0]), pick(rs[0])
	for _, r := range rs[1:] {
		lo, hi = min(lo, pick(r)), max(hi, pick(r))
	}
	return [2]float64{lo, hi}
}
