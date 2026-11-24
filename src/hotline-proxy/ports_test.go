package proxy_test

import (
	"context"
	"net"
	"os"
	"strconv"
	"sync"
)

// reservedPorts hands out the ports a test gives the proxy, or routes a
// vendor to so the vendor refuses the connection.
//
// A port the system picks (port 0) and the test then closes can be handed
// out again at once: to the next test server, to another listener, or to
// the proxy's own. That made scenarios fail now and then (two listeners on
// one address, a "refused" vendor that answered). These ports come from a
// range the system never picks by itself, Linux's being 32768 to 60999 and
// macOS's 49152 to 65535, and each is handed out once while the suite
// runs. So nothing in the suite takes one by chance, and a port nothing
// listens on stays refused.
//
// One is made per suite run and shared by every scenario. A suite run again
// in the same process (go test -count) starts over, once the last run's
// servers have closed. A test run on the same machine in another process
// starts at another place in the range, chosen by its process id, and a
// port some other program already uses is skipped.
type reservedPorts struct {
	mu   sync.Mutex
	next int
}

const (
	reservedFirst = 20000
	reservedCount = 12000
)

func newReservedPorts() *reservedPorts {
	return &reservedPorts{next: os.Getpid() % reservedCount}
}

// take is a local address with a port no one else in this run is given,
// where nothing listens yet.
func (r *reservedPorts) take() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for range reservedCount {
		port := reservedFirst + r.next
		r.next = (r.next + 1) % reservedCount
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		if l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr); err == nil {
			_ = l.Close()
			return addr
		}
	}
	panic("no reserved port is free")
}
