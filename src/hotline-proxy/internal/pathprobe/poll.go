//go:build linux || darwin

package pathprobe

import "hotline-proxy/internal/fits"

// pollable is fd as poll takes it, in 32 bits, or ErrUnusableSocket when
// the system numbered the socket beyond that.
func pollable(fd int) (int32, error) {
	n, fit := fits.Int32(fd)
	if !fit {
		return 0, ErrUnusableSocket
	}
	return n, nil
}
