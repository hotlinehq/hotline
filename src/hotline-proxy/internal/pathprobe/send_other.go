//go:build !linux && !darwin

package pathprobe

import (
	"context"
	"errors"
)

// Send sends nothing: path probes are built for Linux and macOS only.
func (Sender) Send(context.Context, Packet) Answer {
	return notSent(errors.ErrUnsupported)
}
