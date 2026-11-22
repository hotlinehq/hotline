package config

import "time"

// Probe says how the proxy probes a vendor endpoint after calls to it fail
// to connect (docs/decisions/probe.md): whether it does, how long it waits
// between probes while the failures go on, and how long each step of a
// probe may take.
type Probe struct {
	Enabled bool
	// First is the wait after the first probe. Each next wait doubles, up
	// to Longest. The waits end, and the next failure probes at once
	// again, when no call has failed for QuietAfter.
	First      time.Duration
	Longest    time.Duration
	QuietAfter time.Duration
	// Timeout is how long each step of a probe (DNS, the TCP connect, TLS,
	// each packet of the path trace) may take.
	Timeout time.Duration
	// MaxHops is how many routers the path trace goes through at most.
	MaxHops int
}

// maxHopsLimit is the most routers a path trace may go through: a TTL
// above 64 reaches anything on the internet.
const maxHopsLimit = 64

// probeDefaults are the settings of an api that says nothing about probes,
// and of every host outside the apis (Peter, 2026-11-21).
func probeDefaults() Probe {
	return Probe{Enabled: true, First: time.Minute, Longest: 15 * time.Minute, QuietAfter: 15 * time.Minute, Timeout: 2 * time.Second, MaxHops: 20}
}

// probeFile is an api's probe section as the config writes it.
type probeFile struct {
	Enabled *bool `yaml:"enabled"`
	Backoff struct {
		First      *string `yaml:"first"`
		Longest    *string `yaml:"longest"`
		QuietAfter *string `yaml:"quietAfter"`
	} `yaml:"backoff"`
	ProbeTimeout *string `yaml:"probeTimeout"`
	MaxHops      *int    `yaml:"maxHops"`
}

// probe reads an api's probe section, or gives the defaults.
func (l *loader) probe(label string, f *probeFile) Probe {
	p := probeDefaults()
	if f == nil {
		return p
	}
	label += ": probe"
	if f.Enabled != nil {
		p.Enabled = *f.Enabled
	}
	p.First = l.interval(label, "backoff.first", f.Backoff.First, p.First)
	p.Longest = l.interval(label, "backoff.longest", f.Backoff.Longest, p.Longest)
	p.QuietAfter = l.interval(label, "backoff.quietAfter", f.Backoff.QuietAfter, p.QuietAfter)
	p.Timeout = l.interval(label, "probeTimeout", f.ProbeTimeout, p.Timeout)
	if f.MaxHops != nil {
		p.MaxHops = *f.MaxHops
	}
	if p.MaxHops < 1 || p.MaxHops > maxHopsLimit {
		l.problem("%s: maxHops %d must be from 1 to %d", label, p.MaxHops, maxHopsLimit)
	}
	if p.First > p.Longest {
		l.problem("%s: backoff.first %s is longer than backoff.longest %s", label, p.First, p.Longest)
	}
	return p
}

// ProbeFor is how the proxy probes a host. When several apis reach the host,
// the most careful setting wins: off when any is off, the longest of each
// wait (Peter, 2026-11-21), and the fewest routers to trace. A host outside the apis has the defaults.
func (c *Config) ProbeFor(host string) Probe {
	if p, found := c.probes[host]; found {
		return p
	}
	return probeDefaults()
}

// carefuller is the more careful of two probe settings of one host.
func carefuller(a, b Probe) Probe {
	return Probe{
		Enabled:    a.Enabled && b.Enabled,
		First:      max(a.First, b.First),
		Longest:    max(a.Longest, b.Longest),
		QuietAfter: max(a.QuietAfter, b.QuietAfter),
		Timeout:    max(a.Timeout, b.Timeout),
		MaxHops:    min(a.MaxHops, b.MaxHops),
	}
}
