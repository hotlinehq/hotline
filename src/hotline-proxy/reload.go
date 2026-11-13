package proxy

import (
	"fmt"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"

	"hotline-proxy/internal/config"
)

// reloader loads the config again when asked to, and switches the proxy
// over only when all of it is valid.
type reloader struct {
	path      string
	clock     Clock
	lookupEnv func(string) (string, bool)
	logger    *slog.Logger
	own       *own
	// started is the config the proxy started with: settings that need a
	// restart are still as it says.
	started *config.Config

	// mu lets one check or reload run at a time.
	mu sync.Mutex
	// seen are the files of the last load, good or not, as they were then.
	seen map[string]os.FileInfo
}

// CheckFiles reloads the config when one of its files changed since the
// last load: the config, a spec, an overlay, a key file or a certificate.
// A file counts as changed when it is another file than before, or its
// time or size changed; it is not read otherwise.
func (p *Proxy) CheckFiles() {
	r := p.reload
	r.mu.Lock()
	defer r.mu.Unlock()
	for path, before := range r.seen {
		if now, _ := os.Stat(path); !same(before, now) {
			p.load()
			return
		}
	}
}

// Reload loads the config again now, whether or not a file changed.
func (p *Proxy) Reload() {
	p.reload.mu.Lock()
	defer p.reload.mu.Unlock()
	p.load()
}

// load reads and checks everything, and switches over only when all of it
// is valid. Calls in progress finish on the config they started with.
func (p *Proxy) load() {
	r := p.reload
	// The files are looked at before they are read, so a change made
	// while they are read is seen by the next check.
	old := p.handler.cfg.Load()
	before := stat(old.Files)
	cfg, err := config.Load(r.path, r.clock.Now(), r.lookupEnv)
	// From now on the files of this load are watched, and after a failed
	// one also those of the config still in use.
	watched := cfg.Files
	if err != nil {
		watched = append(watched, old.Files...)
	}
	r.seen = map[string]os.FileInfo{}
	for _, path := range watched {
		info, known := before[path]
		if !known {
			info, _ = os.Stat(path)
		}
		r.seen[path] = info
	}
	if err != nil {
		// One line per problem, so a config with many lists each.
		for _, problem := range strings.Split(err.Error(), "\n") {
			r.logger.Warn("config reload failed, the old config stays in use", "problem", problem)
		}
		r.own.reloaded(false, false)
		return
	}
	for _, setting := range needRestart(old, cfg) {
		r.logger.Warn(setting + " changed and needs a restart; the rest of the config is in use")
	}
	for _, w := range cfg.Warnings {
		r.logger.Warn("config warning", "reason", w)
	}
	if p.adminTLS != nil && cfg.Admin != nil && cfg.Admin.TLS != nil && cfg.Admin.TLS.Sign == r.started.Admin.TLS.Sign {
		p.adminTLS.set(cfg.Admin.TLS)
	}
	for _, li := range p.listeners {
		li.reload(cfg.Listener(li.name))
	}
	p.handler.cfg.Store(cfg)
	p.handler.checks.resize(cfg.CheckQueueMemory)
	r.own.reloaded(true, len(needRestart(r.started, cfg)) > 0)
	r.logger.Info("config reloaded", "config_version", cfg.Label())
}

// needRestart names the settings that changed but take effect only on a
// restart: the listeners added, removed or changed in shape, the admin
// port, and where the proxy sends records.
func needRestart(old, cfg *config.Config) []string {
	var changed []string
	if adminShape(old.Admin) != adminShape(cfg.Admin) {
		changed = append(changed, "admin")
	}
	shapes := map[string]string{}
	for _, l := range old.Listeners {
		shapes[l.Name] = l.Shape()
	}
	for _, l := range cfg.Listeners {
		if before, kept := shapes[l.Name]; !kept || before != l.Shape() {
			changed = append(changed, "listener "+l.Name)
		}
		delete(shapes, l.Name)
	}
	for _, name := range slices.Sorted(maps.Keys(shapes)) {
		changed = append(changed, "listener "+name)
	}
	if !reflect.DeepEqual(old.Exporters, cfg.Exporters) || !reflect.DeepEqual(old.FileExporters, cfg.FileExporters) || !reflect.DeepEqual(old.Console, cfg.Console) {
		changed = append(changed, "exporters")
	}
	if !reflect.DeepEqual(old.Telemetry, cfg.Telemetry) {
		changed = append(changed, "telemetry")
	}
	if old.Pprof != cfg.Pprof {
		changed = append(changed, "telemetry.pprof")
	}
	return changed
}

// stat looks up each file; a file that is missing is kept as nil, so its
// arrival counts as a change.
func stat(paths []string) map[string]os.FileInfo {
	seen := make(map[string]os.FileInfo, len(paths))
	for _, path := range paths {
		seen[path], _ = os.Stat(path)
	}
	return seen
}

// same tells whether a file is still the same file, as it was: Kubernetes
// swaps a mounted file for a new one, and an editor writes in place.
func same(a, b os.FileInfo) bool {
	return a == nil && b == nil ||
		a != nil && b != nil && os.SameFile(a, b) && a.ModTime().Equal(b.ModTime()) && a.Size() == b.Size()
}

// adminShape is what a reload cannot change on the admin port.
func adminShape(a *config.Admin) string {
	if a == nil {
		return ""
	}
	return fmt.Sprintf("%s tls=%t sign=%t", a.URL(), a.TLS != nil, a.TLS != nil && a.TLS.Sign)
}
