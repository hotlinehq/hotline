//go:build !unix

package main

import "os"

// terminal reads no terminal on this system: the console is never
// coloured unless the config says always.
func terminal(*os.File) func() (int, bool) {
	return func() (int, bool) { return 0, false }
}
