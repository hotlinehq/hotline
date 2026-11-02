package proxy

import (
	"sync"

	"hotline-proxy/internal/spec"
)

// checkLine runs the spec checks of every call on a few workers, one per
// processor core, apart from the calls, so a busy proxy never slows a call
// down for its checks. A check that finds every worker busy waits in a
// line, holding its body's copy in memory, never on disk. The memory the
// waiting bodies hold is capped: when a new check does not fit, the oldest
// waiting ones give way and are skipped as overloaded (Peter, 2026-11-02,
// found by the load test).
type checkLine struct {
	clock Clock
	// dropped counts each check skipped as overloaded.
	dropped func()

	mu      sync.Mutex
	ready   *sync.Cond
	waiting []*checkJob
	// memory is what the waiting bodies hold, limit how much they may.
	memory int64
	limit  int64
	// idle are the workers not running a check, counted from the start,
	// before a worker first runs.
	idle int
	// later are the checks that join the line once another check ends;
	// the workers wait for them before they stop.
	later  int
	closed bool
}

// checkJob is one side of a call's check, waiting for a worker.
type checkJob struct {
	check *specCheck
	run   func() spec.Checked
	// release gives the body's buffer back when the check is dropped.
	release func()
	// bytes is the memory the body holds while the check waits.
	bytes int64
	// late marks a check that joins the line once another check ends.
	late bool
}

func newCheckLine(workers int, limit int64, clock Clock, dropped func()) *checkLine {
	l := &checkLine{clock: clock, dropped: dropped, limit: limit, idle: workers}
	l.ready = sync.NewCond(&l.mu)
	for range workers {
		go l.work()
	}
	return l
}

// add hands a check to the line. A free worker takes it whatever its size;
// otherwise it waits, and the oldest waiting bodies make room for it. A
// body larger than the whole cap cannot wait, so its check is skipped.
func (l *checkLine) add(job *checkJob) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if job.late {
		// Workers may be waiting for this check alone, so they stop.
		l.later--
		l.ready.Broadcast()
	}
	if l.idle > len(l.waiting) {
		// A free worker takes it, so it never waits: its body does not
		// count against the cap, and no later check pushes it out.
		job.bytes = 0
	} else {
		if job.bytes > l.limit {
			l.drop(job)
			return
		}
		l.trim(l.limit - job.bytes)
	}
	l.waiting = append(l.waiting, job)
	l.memory += job.bytes
	l.ready.Signal()
}

// resize sets a new cap, from a reloaded config. Waiting bodies beyond it
// give way at once.
func (l *checkLine) resize(limit int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.limit = limit
	l.trim(limit)
}

// trim drops the oldest waiting checks that hold a body until the memory
// held is at most limit. A limit below 0 drops every waiting check.
func (l *checkLine) trim(limit int64) {
	kept := l.waiting[:0]
	for _, job := range l.waiting {
		if l.memory > limit && (job.bytes > 0 || limit < 0) {
			l.memory -= job.bytes
			l.drop(job)
			continue
		}
		kept = append(kept, job)
	}
	clear(l.waiting[len(kept):])
	l.waiting = kept
}

// drop skips a check as overloaded: its call's record goes out without it.
func (l *checkLine) drop(job *checkJob) {
	now := l.clock.Now()
	s := job.check
	s.found, s.dropped, s.waited, s.end = spec.Checked{Skipped: "overloaded"}, true, now, now
	job.release()
	l.dropped()
	close(s.done)
}

// expect says a check will join the line once another check ends.
func (l *checkLine) expect() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.later++
}

// stop lets the workers end once no check waits or is still to come. When
// the time to stop ran out, now drops the checks still waiting, so their
// calls send their records at once, and says how many.
func (l *checkLine) stop(now bool) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	waiting := len(l.waiting)
	if now {
		l.trim(-1)
	}
	l.ready.Broadcast()
	return waiting - len(l.waiting)
}

// held is the memory the waiting bodies hold, and how many checks wait.
func (l *checkLine) held() (memory int64, waiting int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.memory, len(l.waiting)
}

// work runs the oldest waiting check, one at a time, until the line stops.
func (l *checkLine) work() {
	for {
		l.mu.Lock()
		for len(l.waiting) == 0 && (!l.closed || l.later > 0) {
			l.ready.Wait()
		}
		if len(l.waiting) == 0 {
			l.mu.Unlock()
			return
		}
		job := l.waiting[0]
		l.waiting[0] = nil
		l.waiting = l.waiting[1:]
		l.memory -= job.bytes
		l.idle--
		l.mu.Unlock()
		job.check.waited = l.clock.Now()
		job.check.found = job.run()
		job.check.end = l.clock.Now()
		close(job.check.done)
		l.mu.Lock()
		l.idle++
		l.mu.Unlock()
	}
}
