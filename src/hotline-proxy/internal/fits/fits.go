// Package fits converts a number to a smaller type only when it fits, so
// a value never wraps around silently. Each function says false when the
// number does not fit, and the caller decides what that means where it is.
//
// The functions are small enough for the Go compiler to copy them into
// their callers, so a call costs the same as the check written out by hand.
// The test checks that this stays true.
package fits

import "math"

// Uint8 is n as a byte, and false when n is negative or above 255.
func Uint8(n int) (uint8, bool) {
	if n < 0 || n > math.MaxUint8 {
		return 0, false
	}
	return uint8(n), true
}

// Uint16 is n as a uint16, such as a port, and false when n does not fit.
func Uint16(n int) (uint16, bool) {
	if n < 0 || n > math.MaxUint16 {
		return 0, false
	}
	return uint16(n), true
}

// Uint32 is n as a uint32, such as a length written in 4 bytes, and false
// when n does not fit.
func Uint32(n int) (uint32, bool) {
	if n < 0 || n > math.MaxUint32 {
		return 0, false
	}
	return uint32(n), true
}

// Int32 is n as an int32, such as a file descriptor for poll, and false
// when n does not fit.
func Int32(n int) (int32, bool) {
	if n < math.MinInt32 || n > math.MaxInt32 {
		return 0, false
	}
	return int32(n), true
}

// Uint64 is n as a uint64, and false when n is negative.
func Uint64(n int) (uint64, bool) {
	if n < 0 {
		return 0, false
	}
	return uint64(n), true
}

// Int64 is an unsigned n, such as one read from a file, as an int64, and
// false when it is above the largest int64.
func Int64(n uint64) (int64, bool) {
	if n > math.MaxInt64 {
		return 0, false
	}
	return int64(n), true
}

// Int is an unsigned n, such as a count or a length read from a file, as
// an int, and false when it is above the largest int.
func Int(n uint64) (int, bool) {
	if n > math.MaxInt {
		return 0, false
	}
	return int(n), true
}
