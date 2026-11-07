// Command load runs the Hotline load test: see load/README.md.
package main

import (
	"flag"
	"fmt"
	"os"

	"hotline-load/run"
)

func main() {
	var cfg run.Config
	flag.StringVar(&cfg.Proxy, "proxy", "", "the built hotline-proxy program (required)")
	flag.StringVar(&cfg.Spec, "spec", "", "the spec the proxy checks calls against: SSM's, or S3's for xml (required)")
	flag.StringVar(&cfg.K6, "k6", "", "the k6 program (default: the one on the path)")
	flag.StringVar(&cfg.OutDir, "out", "load/results", "where the run folders go")
	flag.StringVar(&cfg.Shape, "shape", "small-json", "the call shape: small-json, medium-json, large-json, huge-json, slow or xml")
	flag.IntVar(&cfg.Rate, "rate", 500, "calls a second")
	flag.StringVar(&cfg.Duration, "duration", "20s", "how long each repeat lasts")
	flag.IntVar(&cfg.Repeats, "repeats", 3, "how many times direct and proxy are each run")
	flag.StringVar(&cfg.Label, "label", "", "names what is measured (for example main or branch), kept in every result")
	flag.BoolVar(&cfg.Profile, "profile", false, "after the repeats, save a processor and a memory profile (needs a proxy with the telemetry.pprof setting)")
	// The workflow sets the environment variable, so the Makefile lines need no flag.
	interval := os.Getenv("HOTLINE_LOAD_METRIC_INTERVAL")
	flag.StringVar(&cfg.MetricInterval, "metric-interval", interval, "how often the proxy sends its own numbers, such as 60s (default 1s, or HOTLINE_LOAD_METRIC_INTERVAL); above 1s needs a proxy with the telemetry.pprof setting")
	flag.StringVar(&cfg.MoreSpecs, "more-specs", "", "a folder of specs, one folder each (for example specs/aws), that the proxy loads as well; calls still go to -spec's api")
	flag.Parse()
	if cfg.Proxy == "" || cfg.Spec == "" {
		fmt.Fprintln(os.Stderr, "-proxy and -spec are required")
		os.Exit(2)
	}
	cfg.Out = os.Stdout
	dir, err := run.Run(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load test failed:", err)
		os.Exit(1)
	}
	fmt.Println("results kept in", dir)
}
