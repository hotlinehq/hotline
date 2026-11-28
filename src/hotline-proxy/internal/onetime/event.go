// Package onetime holds an event that happens once and then stays
// happened, which many open calls and tunnels wait on.
package onetime

import (
	"context"
	"sync"
)

// Event is something that happens once and, once it has happened,
// stays happened, such as "the proxy stops taking tunnels" or "the stop
// cuts the connections still open". It is a channel closed once, so a
// long-lived struct can hold it, where Go's guidance keeps contexts out of
// structs (Peter, 2026-11-24, Q3 and Q4).
//
// What is to run when it comes is kept in a list, as context.AfterFunc
// does for a context. Every open call waits on the stop's cut, so a wait
// must cost no goroutine of its own: one per call showed in the load test
// profiles (Peter, 2026-11-25, Q1).
type Event struct {
	done chan struct{}

	mu      sync.Mutex
	waiting map[*func()]struct{} // nil once the event has come
}

// NewEvent is an event that has not come yet.
func NewEvent() *Event {
	return &Event{done: make(chan struct{}), waiting: map[*func()]struct{}{}}
}

// Fire makes the event come and starts what waits on it; firing it again
// does nothing. Each waiting function runs in a goroutine of its own, as
// with context.AfterFunc, so one that is slow, such as a Close that still
// sends to the other side, holds up neither fire nor the others.
func (s *Event) Fire() {
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

// Fired is closed once the event has come.
func (s *Event) Fired() <-chan struct{} {
	return s.done
}

// AfterFire runs f once the event comes, unless stop is called first. If
// the event has already come, f starts at once. It is context.AfterFunc
// for an event. stop keeps f from running if the event has not come yet;
// if the event is already coming, f may still run after stop returns, and
// stop does not wait for it.
func (s *Event) AfterFire(f func()) (stop func()) {
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

// Context is a context under parent that also ends when the event comes,
// for the calls that take a context; cancel releases it.
func (s *Event) Context(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := s.AfterFire(cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
