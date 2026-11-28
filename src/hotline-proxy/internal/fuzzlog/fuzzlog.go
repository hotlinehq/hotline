// Package fuzzlog keeps what a fuzz worker writes. Go's fuzzing runs each
// input in worker processes and sends their output nowhere, so a worker
// that crashes leaves only "fuzzing process hung or terminated
// unexpectedly", with no reason. A worker that calls Capture writes its
// output, a crash's stack trace among it, to a file of its own instead
// (docs/decisions/fuzzing.md). It is for fuzz tests only.
package fuzzlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dirVariable names the environment variable that holds the folder for
// the workers' files. Unset, Capture does nothing.
const dirVariable = "HOTLINE_FUZZ_LOGS"

// Capture sends this process's standard error to a file in the folder
// dirVariable names, when the process is a fuzz worker. Call it first in
// each Fuzz function. Any failure leaves the output where it was: a fuzz
// run must not stop over its logs.
func Capture() {
	path, ok := Path(os.Args, os.Getenv(dirVariable), os.Getpid())
	if !ok {
		return
	}
	f, err := os.Create(path) //nolint:gosec // the path is the workflow's own folder
	if err != nil {
		return
	}
	// The file stays open for the life of the process, as its standard
	// error.
	_ = redirectStderr(f)
}

// Path is the file for the output of the process with args and pid, in dir.
// It is false for a process that is not a fuzz worker, or when dir is
// empty.
func Path(args []string, dir string, pid int) (string, bool) {
	if dir == "" || !isWorker(args) {
		return "", false
	}
	return filepath.Join(dir, fmt.Sprintf("worker-%d.log", pid)), true
}

// isWorker says whether args are those Go starts a fuzz worker with.
func isWorker(args []string) bool {
	for _, a := range args {
		if a == "-test.fuzzworker" || strings.HasPrefix(a, "-test.fuzzworker=") {
			return true
		}
	}
	return false
}
