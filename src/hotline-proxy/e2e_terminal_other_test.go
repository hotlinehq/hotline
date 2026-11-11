//go:build e2e && !linux

package proxy_test

import "errors"

// openTerminal makes no terminal here: the test makes one on Linux only.
func openTerminal(int) (*terminal, error) {
	return nil, errors.New("a test terminal can be made on Linux only")
}
