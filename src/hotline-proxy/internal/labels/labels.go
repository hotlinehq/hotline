// Package labels keeps the label sets of the proxy's own numbers. Making a
// set sorts and hashes its labels, and the proxy made one for every call,
// though only a few different ones exist: the numbers are never split by
// anything that grows with the calls (Peter, 2026-10-31).
package labels

import (
	"sync"

	"go.opentelemetry.io/otel/attribute"
)

// Room is how many labels a kept set holds at most; a list of more is made
// fresh each time.
const Room = 4

// Most is how many sets are kept at most, so labels that turn out to vary
// more than expected, such as a vendor's odd status codes, cost memory once
// and no more.
const Most = 1000

// key is a list of labels as a value that can be looked up. Labels are
// compared in the order given, so the same labels in another order make a
// second, equal set: harmless. Unused places are empty labels, so a list
// ending in an empty label finds the shorter list's set; the proxy never
// labels a number with an empty label.
type key [Room]attribute.KeyValue

// Kept is a store of label sets; its zero value is ready to use. One lock
// guards it: a look-up is short, and a set is made only the first time.
type Kept struct {
	mu   sync.Mutex
	sets map[key]attribute.Set
}

// Set is the label set for kvs, made the first time and kept after.
func (k *Kept) Set(kvs ...attribute.KeyValue) attribute.Set {
	if len(kvs) > Room {
		return attribute.NewSet(kvs...)
	}
	var at key
	copy(at[:], kvs)
	k.mu.Lock()
	defer k.mu.Unlock()
	if set, ok := k.sets[at]; ok {
		return set
	}
	set := attribute.NewSet(kvs...)
	if k.sets == nil {
		k.sets = make(map[key]attribute.Set)
	}
	if len(k.sets) < Most {
		k.sets[at] = set
	}
	return set
}

// Len is how many sets are kept.
func (k *Kept) Len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.sets)
}
