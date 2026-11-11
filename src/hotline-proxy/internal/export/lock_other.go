//go:build !unix

package export

import "os"

// lockExclusive takes no lock where the system has no flock; the proxy is
// built for Linux and macOS only.
func lockExclusive(*os.File) error { return nil }
