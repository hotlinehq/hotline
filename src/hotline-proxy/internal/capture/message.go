// Package capture holds what the capture ingress shares with the macOS
// extension: the messages on its sockets, and the private folder the
// sockets live in. The messages are described in docs/protocol.md of the
// hotline-macos-capture repository, and a change there needs a change here.
package capture

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
)

const (
	// Version is the version of the messages. A message with another
	// version is refused.
	Version = 1
	// MaxFrame is the most a message body may hold, in bytes.
	MaxFrame = 16 * 1024

	// RoleControl is the one connection that carries the app list.
	RoleControl = "control"
	// RoleFlow is a connection that carries one captured call.
	RoleFlow = "flow"

	maxHost        = 253
	maxProcessName = 4096
)

// ErrBadHello is a first message that cannot be used: the wrong size, not
// JSON, another version, or a flow that names no usable destination.
var ErrBadHello = errors.New("capture: the first message is not a usable hello")

// The reasons a hello is refused, each wrapped with what was found.
var (
	errVersion   = errors.New("unknown version")
	errRole      = errors.New("unknown role")
	errTransport = errors.New("transport is not tcp")
	errHost      = errors.New("the host is not a name or an address")
	errPort      = errors.New("port out of range")
	errPID       = errors.New("negative process number")
	errPath      = errors.New("the program path is too long")
	errSize      = errors.New("message over the size limit")
)

// Hello is the first message of every connection from the extension.
type Hello struct {
	Version int    `json:"version"`
	Role    string `json:"role"`
	// The rest is only for a flow: the destination as the program asked
	// for it (a name or an address) and the program.
	Transport   string `json:"transport,omitempty"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	PID         int    `json:"pid,omitempty"`
	ProcessName string `json:"processName,omitempty"`
}

// SpecUpdate is the app list Hotline sends on the control connection.
type SpecUpdate struct {
	Version int      `json:"version"`
	Entries []string `json:"entries"`
}

// ReadHello reads the first message from r and checks it. It reads no more
// than the message, so what follows stays in r for the connection.
func ReadHello(r *bufio.Reader) (Hello, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return Hello{}, fmt.Errorf("%w: %w", ErrBadHello, err)
	}
	n := binary.BigEndian.Uint32(size[:])
	if n > MaxFrame {
		return Hello{}, fmt.Errorf("%w: %w: %d bytes, at most %d", ErrBadHello, errSize, n, MaxFrame)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return Hello{}, fmt.Errorf("%w: %w", ErrBadHello, err)
	}
	var h Hello
	if err := json.Unmarshal(body, &h); err != nil {
		return Hello{}, fmt.Errorf("%w: %w", ErrBadHello, err)
	}
	if err := h.check(); err != nil {
		return Hello{}, fmt.Errorf("%w: %w", ErrBadHello, err)
	}
	return h, nil
}

func (h Hello) check() error {
	if h.Version != Version {
		return fmt.Errorf("%w: %d, want %d", errVersion, h.Version, Version)
	}
	switch h.Role {
	case RoleControl:
		return nil
	case RoleFlow:
	default:
		return fmt.Errorf("%w: %q", errRole, h.Role)
	}
	switch {
	case h.Transport != "tcp":
		return fmt.Errorf("%w: %q", errTransport, h.Transport)
	case !validHost(h.Host):
		return errHost
	case h.Port < 1 || h.Port > 65535:
		return fmt.Errorf("%w: %d", errPort, h.Port)
	case h.PID < 0:
		return fmt.Errorf("%w: %d", errPID, h.PID)
	case len(h.ProcessName) > maxProcessName:
		return errPath
	}
	return nil
}

// validHost says whether host is an address, or a name of printable
// characters with no slash, space or colon.
func validHost(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	if host == "" || len(host) > maxHost || strings.ContainsAny(host, "/: ") {
		return false
	}
	for _, c := range host {
		if c < 0x21 || c == 0x7f {
			return false
		}
	}
	return true
}

// Spec is the app list to send for apps, which always ends with a skip rule
// for Hotline's own process, so its calls to vendors are never captured
// again. apps must not be empty: a list holding only the skip rule would
// capture everything else.
func Spec(apps []string, ownPID int) SpecUpdate {
	entries := append(append([]string{}, apps...), fmt.Sprintf("!%d", ownPID))
	return SpecUpdate{Version: Version, Entries: entries}
}

// Frame writes v as a message: 4 bytes with the size of the body, then the
// body as JSON.
func Frame(v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxFrame {
		return nil, fmt.Errorf("capture: %w: %d bytes, at most %d", errSize, len(body), MaxFrame)
	}
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...), nil //nolint:gosec // checked above
}
