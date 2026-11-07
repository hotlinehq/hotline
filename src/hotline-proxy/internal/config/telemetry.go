package config

import (
	"slices"
	"strings"
	"time"
)

// Telemetry is the destination of the proxy's own numbers, and the metrics
// turned off by name.
type Telemetry struct {
	Exporter Exporter
	// Disable are metric names, each exact or ending in * for every name
	// that starts with what is before it.
	Disable []string
}

// telemetryFile is the telemetry section as the config writes it.
type telemetryFile struct {
	OTLP    *exporterFile `yaml:"otlp"`
	Disable []string      `yaml:"disable"`
	// Pprof serves Go's profile pages on the admin port.
	Pprof bool `yaml:"pprof"`
}

// OwnMetrics are the names of every metric the proxy sends about itself, so
// a name in telemetry.disable that matches none of them is reported.
func OwnMetrics() []string {
	return []string{
		"http.server.active_requests",
		"http.server.request.duration",
		"hotline.app_tls.failures",
		"hotline.exporter.up",
		"hotline.exporter.queue.size",
		"hotline.exporter.sent",
		"hotline.exporter.dropped",
		"hotline.config.reload_failed",
		"hotline.config.restart_needed",
		"hotline.certificate.not_after",
		"go.memory.used",
		"go.memory.allocated",
		"go.memory.limit",
		"go.memory.gc.goal",
		"go.goroutine.count",
	}
}

// Matches says whether a name from telemetry.disable covers a metric.
func Matches(pattern, metric string) bool {
	if prefix, group := strings.CutSuffix(pattern, "*"); group {
		return strings.HasPrefix(metric, prefix)
	}
	return pattern == metric
}

// telemetry reads the telemetry section. Without one, or without its otlp
// part, nothing of the proxy's own is sent, and that is a warning.
func (l *loader) telemetry(f *telemetryFile, now time.Time) *Telemetry {
	if f == nil || f.OTLP == nil {
		l.warnings = append(l.warnings, "there is no telemetry section, so the proxy's own telemetry is not sent")
		return nil
	}
	t := &Telemetry{Exporter: l.exporter(*f.OTLP, now), Disable: f.Disable}
	// The destination gets only metrics.
	if f.OTLP.LogInterval != nil {
		l.warnings = append(l.warnings, "telemetry.otlp.logInterval is ignored: the telemetry destination gets only metrics")
	}
	if f.OTLP.TraceInterval != nil {
		l.warnings = append(l.warnings, "telemetry.otlp.traceInterval is ignored: the telemetry destination gets only metrics")
	}
	for _, name := range f.Disable {
		if !slices.ContainsFunc(OwnMetrics(), func(metric string) bool { return Matches(name, metric) }) {
			l.warnings = append(l.warnings, "telemetry.disable: "+name+" matches none of the proxy's metrics, and is ignored")
		}
	}
	return t
}
