//go:build race

package santhosh_test

// raceEnabled is true when the tests run under Go's race detector, which
// makes every memory allocation larger and so spoils memory limits.
const raceEnabled = true
