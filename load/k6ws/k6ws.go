// Package k6ws adds k6/x/hotline-live to k6: one Gemini Live session per
// call, run by hotline-load/live. Stock k6 opens WebSockets with
// gorilla/websocket, which can only pack each message alone; this one uses
// coder/websocket, which can also pack against earlier messages, as real
// apps do. Built into k6 with xk6, see load/README.md.
//
// It only passes the call on: everything it does is in hotline-load/live,
// tested there.
package k6ws

import (
	"go.k6.io/k6/js/modules"

	"hotline-load/live"
)

func init() { //nolint:gochecknoinits // how k6 finds an extension
	modules.Register("k6/x/hotline-live", new(root))
}

type root struct{}

// NewModuleInstance is called once for each virtual user.
func (*root) NewModuleInstance(vu modules.VU) modules.Instance { return &instance{vu: vu} }

type instance struct{ vu modules.VU }

// Exports gives the script session(options), which runs one session and
// returns what it saw.
func (i *instance) Exports() modules.Exports {
	return modules.Exports{Named: map[string]any{"session": i.session}}
}

func (i *instance) session(o live.Options) (live.Stats, error) {
	return live.Session(i.vu.Context(), o)
}
