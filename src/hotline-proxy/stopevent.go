package proxy

import (
	"context"
	"sync"
)

// stopEvent is a moment of the proxy's stop that, once it has come, stays
// come, such as "the proxy stops taking tunnels" or "the stop cuts what is
// still open". It is a channel closed once, so a long-lived struct can
// hold it, where Go's guidance keeps contexts out of structs (Peter,
// 2026-11-24, Q3).
type stopEvent struct {
	done chan struct{}
	once sync.Once
}

func newStopEvent() *stopEvent {
	return &stopEvent{done: make(chan struct{})}
}

// fire makes the event come; firing it again does nothing.
func (s *stopEvent) fire() {
	s.once.Do(func() { close(s.done) })
}

// fired is closed once the event has come.
func (s *stopEvent) fired() <-chan struct{} {
	return s.done
}

// afterFire runs f once the event comes, unless stop is called first.
// It is context.AfterFunc for an event; stop waits for nothing.
func (s *stopEvent) afterFire(f func()) (stop func()) {
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
func (s *stopEvent) context(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := s.afterFire(cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
