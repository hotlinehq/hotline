package proxy_test

import "time"

// The two ways a test waits (docs/flakes.md). A test never waits a fixed
// time and hopes that something has happened by then: it checks again and
// again until it has, up to a limit. The lint check refuses time.Sleep in
// tests, so every wait goes through one of these two.

// waitLimit is how long a test waits for something it expects to happen
// soon. It is generous, as a busy CI machine can be many times slower than
// a laptop; a test that fails only waits longer before it says so.
const waitLimit = 10 * time.Second

// eventually checks done every few milliseconds until it says yes, or
// until limit has passed. It says whether done said yes.
func eventually(limit time.Duration, done func() bool) bool {
	deadline := time.Now().Add(limit)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for !done() {
		if time.Now().After(deadline) {
			return false
		}
		<-tick.C
	}
	return true
}

// pause waits d. It is for a wait the scenario acts out, such as a vendor
// that sends the rest of its answer later, a check that nothing arrives
// within a time, or the gap between two checks; never to let something
// else finish first.
func pause(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	<-t.C
}
