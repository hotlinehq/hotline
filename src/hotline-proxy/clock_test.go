package proxy_test

import (
	"sync"
	"time"

	proxy "hotline-proxy"
)

// fakeClock runs with the real time, but its waits end only when the test
// lets time pass.
type fakeClock struct {
	mu     sync.Mutex
	offset time.Duration
	timers []*fakeTimer
}

type fakeTimer struct {
	clock *fakeClock
	at    time.Time
	f     func()
	done  bool
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) proxy.Timer {
	t := &fakeTimer{clock: c, at: c.Now().Add(d), f: f}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	stopped := !t.done
	t.done = true
	return stopped
}

// waiting says how many waits have not ended yet.
func (c *fakeClock) waiting() int {
	return c.waitingSince(0)
}

// armed says how many waits have been started so far, ended or not.
func (c *fakeClock) armed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// waitingSince says how many waits started after the first n have not
// ended yet, so a test can tell a call's wait from the ones the proxy
// keeps from its start, such as the ledger's batch timer.
func (c *fakeClock) waitingSince(n int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	waiting := 0
	for _, t := range c.timers[n:] {
		if !t.done {
			waiting++
		}
	}
	return waiting
}

// passToNext lets time go by until the next wait ends.
func (c *fakeClock) passToNext() {
	c.mu.Lock()
	next := time.Duration(-1)
	now := time.Now().Add(c.offset)
	for _, t := range c.timers {
		if d := t.at.Sub(now); !t.done && (next < 0 || d < next) {
			next = d
		}
	}
	c.mu.Unlock()
	c.pass(max(next, 0))
}

// pass lets d go by, firing every wait that ends on the way. It says how
// many fired.
func (c *fakeClock) pass(d time.Duration) int {
	c.mu.Lock()
	c.offset += d
	now := time.Now().Add(c.offset)
	var due []func()
	for _, t := range c.timers {
		if !t.done && !t.at.After(now) {
			t.done = true
			due = append(due, t.f)
		}
	}
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
	return len(due)
}
