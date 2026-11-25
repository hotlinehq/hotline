package proxy

// OneTimeEvent lets the small tests in proxy_test reach oneTimeEvent, which
// the proxy keeps to itself.
type OneTimeEvent = oneTimeEvent

func NewOneTimeEvent() *OneTimeEvent { return newOneTimeEvent() }

func (s *oneTimeEvent) Fire()                     { s.fire() }
func (s *oneTimeEvent) AfterFire(f func()) func() { return s.afterFire(f) }
func (s *oneTimeEvent) Fired() <-chan struct{}    { return s.fired() }
