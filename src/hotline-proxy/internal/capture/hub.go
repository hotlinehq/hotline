package capture

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// writeTimeout is how long a send of the app list to the extension may take;
// an extension that does not read must not hold up a config reload.
const writeTimeout = 5 * time.Second

// ErrHubClosed is what Serve says when the hub was closed before the control
// connection could join.
var ErrHubClosed = errors.New("capture: the hub is closed")

// Hub holds the extension's control connections and the app list they were
// last sent.
type Hub struct {
	mu       sync.Mutex
	list     []byte
	controls map[net.Conn]struct{}
	closed   bool
}

// NewHub makes a hub that sends the app list for apps, with Hotline's own
// skip rule added.
func NewHub(apps []string) *Hub {
	h := &Hub{controls: map[net.Conn]struct{}{}}
	h.Update(apps)
	return h
}

// Update makes the app list apps the one to send, and sends it to every
// control connection. A list too large to send is not sent, and the
// extension keeps the old one.
func (h *Hub) Update(apps []string) {
	list, err := Frame(Spec(apps, os.Getpid()))
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.list = list
	for conn := range h.controls {
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := conn.Write(list); err != nil {
			_ = conn.Close()
		}
	}
}

// Serve sends the app list on a new control connection, keeps the connection
// for later lists, and waits until either side ends it. Nothing more is
// expected from the extension on it. It closes conn before it returns.
func (h *Hub) Serve(conn net.Conn, reader io.Reader) error {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Time{})
	if err := h.join(conn); err != nil {
		return err
	}
	defer h.leave(conn)
	buf := make([]byte, 512)
	for {
		if _, err := reader.Read(buf); err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
				return nil
			}
			return err
		}
	}
}

func (h *Hub) join(conn net.Conn) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrHubClosed
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := conn.Write(h.list); err != nil {
		return err
	}
	h.controls[conn] = struct{}{}
	return nil
}

func (h *Hub) leave(conn net.Conn) {
	h.mu.Lock()
	delete(h.controls, conn)
	h.mu.Unlock()
}

// Close ends every control connection and refuses new ones.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for conn := range h.controls {
		_ = conn.Close()
	}
}
