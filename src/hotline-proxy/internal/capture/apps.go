package capture

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

var (
	// ErrNoApps is a request to capture with no apps named.
	ErrNoApps = errors.New("capture: no apps to capture")
	// ErrBadApp is an app that is neither a program name nor a process number.
	ErrBadApp = errors.New("capture: an app must be a program name or a process number, with an optional ! in front to skip it")
	// ErrNotLoopback is an address that other machines could reach.
	ErrNotLoopback = errors.New("capturing apps needs listen.intercept on a loopback address, such as 127.0.0.1, so no other machine can reach it")
)

// IsFrame says whether a connection that starts with first is the
// extension's framed message, not a TLS handshake: a frame starts with the
// size of the body, at most 16 KiB, so its first byte is zero, and a TLS
// record starts with 0x16.
func IsFrame(first byte) bool { return first == 0 }

// CheckApps says whether apps can be sent to the extension: not empty (an
// empty list captures nothing, and one holding only skip rules would capture
// everything else), and each entry a program name or a process number with
// an optional "!" in front.
func CheckApps(apps []string) error {
	if len(apps) == 0 {
		return ErrNoApps
	}
	for _, app := range apps {
		name := strings.TrimPrefix(app, "!")
		if name == "" || strings.ContainsFunc(name, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			return fmt.Errorf("%w: %q", ErrBadApp, app)
		}
	}
	return nil
}

// CheckAddress says whether address, as host:port, can be reached from this
// machine only: a loopback address or localhost.
func CheckAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrNotLoopback, address)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrNotLoopback, address)
}
