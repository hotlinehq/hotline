package proxy

import (
	"context"
	"sync"
)

// oneTimeEvent is something that happens once and, once it has happened,
// stays happened, such as "the proxy stops taking tunnels" or "the stop
// cuts the connections still open". It is a channel closed once, so a
// long-lived struct can hold it, where Go's guidance keeps contexts out of
// structs (Peter, 2026-11-24, Q3 and Q4).
//
// What is to run when it comes is kept in a list, as context.AfterFunc
// does for a context. Every open call waits on the stop's cut, so a wait
// must cost no goroutine of its own: one per call showed in the load test
// profiles (Peter, 2026-11-25, Q1).
type oneTimeEvent struct {
	done chan struct{}

	mu      sync.Mutex
	waiting map[*func()]struct{} // nil once the event has come
}

func newOneTimeEvent() *oneTimeEvent {
	return &oneTimeEvent{done: make(chan struct{}), waiting: map[*func()]struct{}{}}
}

// fire makes the event come and starts what waits on it; firing it again
// does nothing. Each waiting function runs in a goroutine of its own, as
// with context.AfterFunc, so one that is slow, such as a Close that still
// sends to the other side, holds up neither fire nor the others.
func (s *oneTimeEvent) fire() {
	s.mu.Lock()
	waiting := s.waiting
	if waiting != nil {
		s.waiting = nil
		close(s.done)
	}
	s.mu.Unlock()
	for f := range waiting {
		go (*f)()
	}
}

// fired is closed once the event has come.
func (s *oneTimeEvent) fired() <-chan struct{} {
	return s.done
}

// afterFire runs f once the event comes, unless stop is called first. If
// the event has already come, f starts at once. It is context.AfterFunc
// for an event. stop keeps f from running if the event has not come yet;
// if the event is already coming, f may still run after stop returns, and
// stop does not wait for it.
func (s *oneTimeEvent) afterFire(f func()) (stop func()) {
	s.mu.Lock()
	if s.waiting == nil {
		s.mu.Unlock()
		go f()
		return func() {}
	}
	s.waiting[&f] = struct{}{}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		if s.waiting != nil {
			delete(s.waiting, &f)
		}
		s.mu.Unlock()
	}
}

// context is a context under parent that also ends when the event comes,
// for the calls that take a context; cancel releases it.
func (s *oneTimeEvent) context(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := s.afterFire(cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
