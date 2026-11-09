//go:build !race

package proxy_test

// raceEnabled is false when the tests run without Go's race detector.
const raceEnabled = false
