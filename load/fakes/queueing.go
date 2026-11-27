package fakes

import (
	"sync"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
)

// queueing keeps what the proxy says about its exporters' queues: the
// most bytes its disk queues took at once, the longest an item waited,
// and the wait of the oldest item in the last numbers sent.
type queueing struct {
	mu          sync.Mutex
	diskPeak    int64
	longestWait float64
	lastWait    float64
}

func (q *queueing) read(export *colmetrics.ExportMetricsServiceRequest) {
	for _, rm := range export.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				switch m.GetName() {
				case "hotline.exporter.queue.disk.usage":
					// Each point is one signal's queue; together they share one
					// queueDisk.
					total := int64(0)
					for _, p := range m.GetSum().GetDataPoints() {
						total += p.GetAsInt()
					}
					q.mu.Lock()
					q.diskPeak = max(q.diskPeak, total)
					q.mu.Unlock()
				case "hotline.exporter.queue.oldest.age":
					wait := 0.0
					for _, p := range m.GetGauge().GetDataPoints() {
						wait = max(wait, p.GetAsDouble())
					}
					q.mu.Lock()
					q.longestWait = max(q.longestWait, wait)
					q.lastWait = wait
					q.mu.Unlock()
				}
			}
		}
	}
}

// QueueNumbers is the most bytes the proxy's disk queues took at once and the
// longest any item waited in its queues, in seconds, since the last
// ResetQueueing; and how long the oldest item waited in the last numbers.
func (r *Receiver) QueueNumbers() (diskPeak int64, longestWait, lastWait float64) {
	r.queueing.mu.Lock()
	defer r.queueing.mu.Unlock()
	return r.queueing.diskPeak, r.queueing.longestWait, r.queueing.lastWait
}

// ResetQueueing starts the peaks of QueueNumbers again.
func (r *Receiver) ResetQueueing() {
	r.queueing.mu.Lock()
	defer r.queueing.mu.Unlock()
	r.queueing.diskPeak, r.queueing.longestWait = 0, 0
}

// SetBusy has the receiver answer records and spans with 503, as a
// collector in a short outage does, or take them again.
func (r *Receiver) SetBusy(busy bool) { r.busy.Store(busy) }
