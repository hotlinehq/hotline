package onetime_test

import (
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/onetime"
)

// The stop's cut and "no more tunnels" are one-time events that every open
// call or tunnel waits on. A scenario reaches only some of the orders in
// which a wait, its stop and the event can come, so the rest are here.
var _ = Describe("A one-time event", func() {
	sut := eventSUT{}

	It("runs what waits on it when it comes", func() {
		sut.forEvent()
		sut.Wait()
		sut.Fire()
		Expect(sut.Runs()).To(Equal(1))
		Expect(sut.Fired()).To(BeTrue())
	})

	It("runs a wait once, however often it is fired", func() {
		sut.forEvent()
		sut.Wait()
		sut.Fire()
		sut.Fire()
		Expect(sut.Runs()).To(Equal(1))
		Expect(sut.RunsAWhileLater()).To(Equal(1))
	})

	It("does not run a wait that was stopped before it came", func() {
		sut.forEvent()
		sut.Wait()
		sut.StopWaiting()
		sut.Fire()
		Expect(sut.RunsAWhileLater()).To(BeZero())
	})

	It("runs a wait at once when it has already come", func() {
		sut.forEvent()
		sut.Fire()
		sut.Wait()
		Expect(sut.Runs()).To(Equal(1))
	})

	It("lets a wait be stopped after it came", func() {
		sut.forEvent()
		sut.Wait()
		sut.Fire()
		Expect(sut.Runs()).To(Equal(1))
		sut.StopWaiting()
		sut.StopWaiting()
		Expect(sut.RunsAWhileLater()).To(Equal(1))
	})
})

// eventSUT is one event and one wait on it that counts its runs.
type eventSUT struct {
	event *onetime.Event
	runs  *atomic.Int32
	stop  func()
}

// forEvent is an event that has not come, with nothing waiting on it.
func (s *eventSUT) forEvent() {
	s.event, s.runs, s.stop = onetime.NewEvent(), &atomic.Int32{}, func() {}
	DeferCleanup(s.Close)
}

// Close stops the wait, so nothing runs after the test.
func (s *eventSUT) Close() { s.stop() }

// Wait starts a wait that counts each time it runs.
func (s *eventSUT) Wait() { s.stop = s.event.AfterFire(func() { s.runs.Add(1) }) }

// StopWaiting stops the wait.
func (s *eventSUT) StopWaiting() { s.stop() }

// Fire makes the event come.
func (s *eventSUT) Fire() { s.event.Fire() }

// Fired says the event's channel closed, waiting for it a while.
func (s *eventSUT) Fired() bool {
	select {
	case <-s.event.Fired():
		return true
	case <-time.After(waitLimit):
		return false
	}
}

// Runs is how often the wait ran, once it ran or after waiting long
// enough that it should have.
func (s *eventSUT) Runs() int {
	deadline := time.Now().Add(waitLimit)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for s.runs.Load() == 0 && time.Now().Before(deadline) {
		<-tick.C
	}
	return int(s.runs.Load())
}

// RunsAWhileLater is how often the wait ran 50 milliseconds from now,
// time enough for a wrong run to show.
func (s *eventSUT) RunsAWhileLater() int {
	<-time.After(50 * time.Millisecond)
	return int(s.runs.Load())
}

// waitLimit is how long the sut waits for something that should happen
// soon.
const waitLimit = 10 * time.Second
