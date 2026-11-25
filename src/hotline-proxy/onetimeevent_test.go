package proxy_test

import (
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	proxy "hotline-proxy"
)

// The stop's cut and "no more tunnels" are one-time events that every open
// call or tunnel waits on. A scenario reaches only some of the orders in
// which a wait, its stop and the event can come, so the rest are here.
var _ = Describe("A one-time event", func() {
	var (
		event *proxy.OneTimeEvent
		runs  atomic.Int32
		count = func() { runs.Add(1) }
	)
	BeforeEach(func() {
		event = proxy.NewOneTimeEvent()
		runs.Store(0)
	})
	ranOnce := func() bool { return runs.Load() == 1 }

	It("runs what waits on it when it comes", func() {
		defer event.AfterFire(count)()
		event.Fire()
		Expect(eventually(waitLimit, ranOnce)).To(BeTrue())
		Eventually(event.Fired()).Should(BeClosed())
	})

	It("runs a wait once, however often it is fired", func() {
		event.AfterFire(count)
		event.Fire()
		event.Fire()
		Expect(eventually(waitLimit, ranOnce)).To(BeTrue())
		pause(50 * time.Millisecond)
		Expect(runs.Load()).To(Equal(int32(1)))
	})

	It("does not run a wait that was stopped before it came", func() {
		stop := event.AfterFire(count)
		stop()
		event.Fire()
		pause(50 * time.Millisecond)
		Expect(runs.Load()).To(BeZero())
	})

	It("runs a wait at once when it has already come", func() {
		event.Fire()
		defer event.AfterFire(count)()
		Expect(eventually(waitLimit, ranOnce)).To(BeTrue())
	})

	It("lets a wait be stopped after it came", func() {
		stop := event.AfterFire(count)
		event.Fire()
		Expect(eventually(waitLimit, ranOnce)).To(BeTrue())
		stop()
		stop()
		Expect(runs.Load()).To(Equal(int32(1)))
	})
})
