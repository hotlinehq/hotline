// Package labels keeps the label sets of the proxy's numbers. Making a set
// sorts and hashes its labels, and the proxy made one for every call, though
// few different ones come: the numbers are split by ingresses, status codes
// and error types, never by anything that grows with each call (Peter,
// 2026-10-31). The store still does not trust them to stay few, so a
// label that one day comes from the app cannot fill the memory.
package labels

import (
	"sync"

	"go.opentelemetry.io/otel/attribute"
)

// Room is how many labels a kept set holds at most, more than the three a
// call's duration carries; a list of more is made fresh each time.
const Room = 5

// Most is how many sets are kept at most, about 2.5 MB. A full store starts
// again empty, so sets that stop coming, after a reload or from an app that
// makes up ids, do not hold the room for ever; the ones in use are made
// again at their next call.
const Most = 4096

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
	if k.sets == nil || len(k.sets) >= Most {
		k.sets = make(map[key]attribute.Set)
	}
	k.sets[at] = set
	return set
}

// Len is how many sets are kept.
func (k *Kept) Len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.sets)
}
