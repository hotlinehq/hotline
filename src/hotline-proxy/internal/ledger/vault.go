package ledger

import "time"

// Vault is the proxy's in-memory vault for the key batches are signed with
// (Peter, 2026-11-26). It makes a key when it is made, at the proxy's
// start, and a new one once the one in use is life old, and never writes a
// key anywhere. The ledger asks it once for each batch it closes and hands
// the key to the exporters beside the batch, so every copy of a batch is
// signed with the same key. It is not safe for use by two goroutines at
// once: the ledger asks it under its own lock.
type Vault struct {
	life time.Duration
	key  Key
	made time.Time
}

// NewVault is a vault whose keys last life each, with its first key made
// at now.
func NewVault(life time.Duration, now time.Time) *Vault {
	return &Vault{life: life, key: NewKey(), made: now}
}

// Key is the key in use at now: a new one, made at now, when the one in
// use is life old.
func (v *Vault) Key(now time.Time) Key {
	if now.Sub(v.made) >= v.life {
		v.key, v.made = NewKey(), now
	}
	return v.key
}
