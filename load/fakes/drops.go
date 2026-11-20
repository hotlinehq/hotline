package fakes

import (
	"strings"
	"sync"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// drops keeps the proxy's hotline.exporter.dropped: records or spans its
// exporters lost, because a queue overflowed or a send failed. Each point
// is one exporter, signal and reason.
type drops struct {
	mu     sync.Mutex
	points map[string]int64
}

func (d *drops) read(export *colmetrics.ExportMetricsServiceRequest) {
	for _, rm := range export.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				if m.GetName() != "hotline.exporter.dropped" {
					continue
				}
				// A total since the start replaces the last one; growth since
				// the last send is added up.
				growth := m.GetSum().GetAggregationTemporality() == metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
				for _, p := range m.GetSum().GetDataPoints() {
					var key []string
					for _, kv := range p.GetAttributes() {
						key = append(key, kv.GetKey()+"="+kv.GetValue().GetStringValue())
					}
					k := strings.Join(key, ",")
					d.mu.Lock()
					if d.points == nil {
						d.points = map[string]int64{}
					}
					if growth {
						d.points[k] += p.GetAsInt()
					} else {
						d.points[k] = p.GetAsInt()
					}
					d.mu.Unlock()
				}
			}
		}
	}
}

// Dropped is how many records and spans the proxy's exporters have lost
// so far, by its own count (hotline.exporter.dropped).
func (r *Receiver) Dropped() (records, spans int64) {
	r.drops.mu.Lock()
	defer r.drops.mu.Unlock()
	for k, n := range r.drops.points {
		switch {
		case strings.Contains(k, "hotline.signal=record"):
			records += n
		case strings.Contains(k, "hotline.signal=span"):
			spans += n
		}
	}
	return records, spans
}
