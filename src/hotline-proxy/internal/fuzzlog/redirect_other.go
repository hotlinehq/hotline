//go:build !unix

package fuzzlog

import "os"

// redirectStderr does nothing where fuzzing runs nowhere: the workflow
// runs on Linux.
func redirectStderr(*os.File) error { return nil }
