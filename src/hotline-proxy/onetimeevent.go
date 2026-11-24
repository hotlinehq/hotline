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
type oneTimeEvent struct {
	done chan struct{}
	once sync.Once
}

func newOneTimeEvent() *oneTimeEvent {
	return &oneTimeEvent{done: make(chan struct{})}
}

// fire makes the event come; firing it again does nothing.
func (s *oneTimeEvent) fire() {
	s.once.Do(func() { close(s.done) })
}

// fired is closed once the event has come.
func (s *oneTimeEvent) fired() <-chan struct{} {
	return s.done
}

// afterFire runs f once the event comes, unless stop is called first.
// It is context.AfterFunc for an event; stop waits for nothing.
func (s *oneTimeEvent) afterFire(f func()) (stop func()) {
	cancelled := make(chan struct{})
	go func() {
		select {
		case <-s.done:
			f()
		case <-cancelled:
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(cancelled) }) }
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
