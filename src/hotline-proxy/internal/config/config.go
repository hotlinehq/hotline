// Package config reads and checks hotline-proxy.yaml and the specs it points
// to. A config is either valid in full or refused with every problem listed.
package config

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"hotline-proxy/internal/overlay"
	"hotline-proxy/internal/santhosh"
	"hotline-proxy/internal/spec"
	"hotline-proxy/internal/tracectx"
)

// ErrInvalid marks a config the proxy refuses.
var ErrInvalid = errors.New("config: invalid")

// Config is a loaded, checked config. It never changes after Load.
type Config struct {
	// Admin is the admin port; nil means none.
	Admin *Admin
	// Listeners are the ports and socket files the proxy opens, by name.
	Listeners []*Listener
	// Certs are the named cert and key pairs of the certs section, each
	// checked to match, used or not.
	Certs     map[string]*TLS
	Exporters []Exporter
	// FileExporters are the exporters that write calls to a local folder.
	FileExporters []FileExporter
	// Console, when set, prints every call to standard output.
	Console *ConsoleExporter
	// Telemetry, when set, is where the proxy sends numbers about itself;
	// nil means they are not sent.
	Telemetry *Telemetry
	// Pprof turns on Go's profile pages (/debug/pprof/ and /debug/vars) on
	// the admin port, from telemetry.pprof. Off unless set.
	Pprof bool
	// NotPassed applies to calls without a known integration ID, and to
	// calls to a host outside the integration's apis.
	NotPassed Settings
	// CheckQueueMemory is how much memory, in bytes, the bodies of spec
	// checks waiting for a worker may hold (Peter, 2026-11-02).
	CheckQueueMemory int64
	// Name is the config file's name, without its folder.
	Name string
	// Fingerprint is the first 12 hex characters of a SHA-256 of the
	// config, specs and overlays, so every edit gives a new one. Key and certificate files are left out: a
	// renewal is not a new config, and secrets stay out of it.
	Fingerprint string
	// Digest is the whole SHA-256 the fingerprint is cut from, as
	// sha256:<hex>, which each ledger batch carries as hotline.batch.config.
	Digest string
	// Ledger is how records are grouped into signed batches.
	Ledger Ledger
	// Files are every file the load read or tried to read, each as it was
	// just before the load read it (nil when it was not there), so the
	// proxy can tell when one changes, even during the load. A refused
	// config still lists them.
	Files map[string]os.FileInfo
	// Warnings name settings the proxy does not use, such as a misspelt
	// field. They do not stop the proxy.
	Warnings []string
	// integrations are the integrations the config names, by ID, and
	// notPassed the one for every other ID, each with the not-passed
	// entry's settings filled in where it says nothing.
	integrations map[string]*Integration
	notPassed    *Integration
	// probes are the probe settings of each host an api names.
	probes map[string]Probe
}

// Integration is one integration's settings, with its trace, url and
// content settings taken from the not-passed entry where it leaves them
// out, so a call looks it up once (Peter, 2026-11-16). A call to a host
// outside its apis takes the not-passed entry's own Settings (timeout, TLS
// origination, internal IPs) from Config.NotPassed instead, as the handler
// does (spec-checks decision 12, program decision 7).
type Integration struct {
	// Name is the integration's ID, or not-passed for an ID the config
	// does not name. It is the ledger chain its records go in.
	Name string
	// Known says the config names the integration; the not-passed entry
	// itself is not one.
	Known bool
	// APIs are the integration's apis by host, in lower case.
	APIs map[string]*API
	// Trace says how its app sends its trace context, and whether the
	// proxy passes its own on to the vendor.
	Trace Trace
	// URLFull says whether a minimal record carries url.full, the address
	// as sent. It is on whenever Content is full (Peter, 2026-11-12).
	URLFull bool
	// Content says whether its calls are kept in full, with every header
	// and their bodies, for the file exporters, and how much of each body
	// is kept. It is never full without a file exporter.
	Content Content
}

// Integration is the integration with this ID, or the not-passed one for
// an ID the config does not name, an empty one too.
func (c *Config) Integration(id string) *Integration {
	if in, known := c.integrations[id]; known {
		return in
	}
	return c.notPassed
}

// API finds the api the integration uses for a host, or nil when the host
// is outside its apis.
func (i *Integration) API(host string) *API {
	return i.APIs[strings.ToLower(host)]
}

// Ledger is how records are grouped into signed batches (Peter,
// 2026-11-16): a batch closes every Every, or at MaxRecords records.
// Instance, when set, names the proxy in place of a random id per run.
type Ledger struct {
	Every      time.Duration
	MaxRecords int
	Instance   string
}

// Content says whether an integration's calls are kept in full, with every
// header and their bodies, for the file exporters, and how much of each
// body is kept.
type Content struct {
	Full    bool
	MaxBody int64
}

// FileExporter is a folder the proxy writes every call to, one line per
// call (Peter, 2026-11-11).
type FileExporter struct {
	// Path is the folder, with a relative path joined to the config's
	// folder.
	Path string
	// MaxFileSize is the size past which the file is rotated, MaxFiles how
	// many rotated files are kept.
	MaxFileSize int64
	MaxFiles    int
	// Gzip compresses rotated files.
	Gzip bool
	// QueueMemory is how much memory the calls waiting to be written may
	// hold.
	QueueMemory int64
}

// Label is what every record carries as hotline.config.version: the
// config file's name, a +, and the fingerprint.
func (c *Config) Label() string {
	return c.Name + "+" + c.Fingerprint
}

// Trace says how an integration's app sends its trace context, and whether
// the proxy passes its own on to the vendor.
type Trace struct {
	Format  tracectx.Format
	Forward bool
}

// TLS is a certificate the proxy shows apps, or the certificate authority it signs them
// with: the files it comes from, and the pair they held when the config
// was loaded.
type TLS struct {
	CertFile    string
	KeyFile     string
	Certificate *tls.Certificate
}

// Exporter is the setup of one otlp exporter.
type Exporter struct {
	// Endpoint is the collector's address as the config names it.
	Endpoint string
	// URL is where to send. Its scheme is https for TLS, else http. Its
	// path goes in front of /v1/logs and /v1/traces, or /v1/metrics for
	// the telemetry section.
	URL *url.URL
	// CAs are PEM certificates of certificate authorities to trust on top of the proxy's
	// own, for TLS.
	CAs []byte
	// Certificate, when set, is shown to the exporter in mutual TLS.
	Certificate *tls.Certificate
	// Headers go with every request, login headers among them.
	Headers map[string]string
	Gzip    bool
	// H2C sends HTTP/2 from the first byte to a plain http:// endpoint
	// (Peter, 2026-11-08).
	H2C bool
	// QueueSize is how many records wait for the exporter at most.
	QueueSize int
	// LogInterval and TraceInterval are how often records and spans are
	// sent. Both also go sooner once a batch of them waits.
	LogInterval   time.Duration
	TraceInterval time.Duration
	// Resource are labels that describe where the proxy runs, sent to this
	// exporter only. They may change service.name and
	// service.instance.id, never service.version.
	Resource map[string]string
}

// Settings say how a call is made.
type Settings struct {
	TLSOrigination bool
	VendorTimeout  time.Duration
	// AllowInternalIPs lets calls go to loopback, private and other
	// internal IPs, which the proxy otherwise refuses.
	AllowInternalIPs bool
}

// API is one vendor api of an integration.
type API struct {
	Settings
	Spec *spec.Spec
	// Checks are the parts of a call the api checks against its spec: all
	// of them when the config leaves checks out.
	Checks Parts
}

// file is the config file's layout.
type file struct {
	// Listen and Certificate authorities are the old way to write ports and certificate authorities, read
	// only to say they are ignored.
	Listen     yaml.Node                `yaml:"listen"`
	Issuers    yaml.Node                `yaml:"issuers"`
	Admin      *adminFile               `yaml:"admin"`
	Certs      map[string]*pairFile     `yaml:"certs"`
	Listeners  map[string]*listenerFile `yaml:"listeners"`
	Telemetry  *telemetryFile           `yaml:"telemetry"`
	SpecChecks struct {
		QueueMemory string `yaml:"queueMemory"`
	} `yaml:"specChecks"`
	// APIs are the vendor apis, each written once and named by the
	// integrations that use it.
	APIs map[string]struct {
		Hosts []string `yaml:"hosts"`
		Spec  string   `yaml:"spec"`
		// Overlays change the spec for this api only, in this order.
		Overlays []string `yaml:"overlays"`
		// Checks picks the parts of a call to check; left out means all.
		Checks   *[]string    `yaml:"checks"`
		Settings settingsFile `yaml:",inline"`
		// Probe says how the proxy probes the api's hosts when calls fail
		// to connect.
		Probe *probeFile `yaml:"probe"`
	} `yaml:"apis"`
	Exporters []struct {
		OTLP *exporterFile     `yaml:"otlp"`
		File *fileExporterFile `yaml:"file"`
		// Console is a node so that "console:" with nothing after it
		// still counts as a console exporter.
		Console yaml.Node `yaml:"console"`
	} `yaml:"exporters"`
	Integrations []struct {
		ID       string       `yaml:"id"`
		Settings settingsFile `yaml:",inline"`
		APIs     []apiRef     `yaml:"apis"`
		// TraceFormat is how the app sends its trace context; w3c when not
		// set. TraceForward passes the proxy's own on to the vendor.
		TraceFormat  *string `yaml:"traceFormat"`
		TraceForward bool    `yaml:"traceForward"`
		// URLFull puts url.full on this integration's minimal records.
		URLFull *bool `yaml:"urlFull"`
		// Content is records or full; MaxBodySize is how much of each body
		// full keeps.
		Content     string `yaml:"content"`
		MaxBodySize string `yaml:"maxBodySize"`
	} `yaml:"integrations"`
	Ledger struct {
		Batch struct {
			Every      *string `yaml:"every"`
			MaxRecords *int    `yaml:"maxRecords"`
		} `yaml:"batch"`
		Instance *secret `yaml:"instance"`
	} `yaml:"ledger"`
}

// fileExporterFile is one file exporter as the config writes it.
type fileExporterFile struct {
	Path        string `yaml:"path"`
	MaxFileSize string `yaml:"maxFileSize"`
	MaxFiles    *int   `yaml:"maxFiles"`
	Compression string `yaml:"compression"`
	QueueMemory string `yaml:"queueMemory"`
}

// exporterFile is one OTLP exporter as the config writes it.
type exporterFile struct {
	Endpoint    string            `yaml:"endpoint"`
	Headers     map[string]secret `yaml:"headers"`
	TLS         *exporterTLSFile  `yaml:"tls"`
	Compression string            `yaml:"compression"`
	// H2C is true or false, read as text so a wrong value is named.
	H2C string `yaml:"h2c"`
	// Temporality and MetricInterval are read only for the telemetry
	// section. Exporters get no metrics, so there they are ignored
	// (Peter, 2026-11-21).
	Temporality string `yaml:"temporality"`
	QueueSize   *int   `yaml:"queueSize"`
	// The intervals are durations, such as 15s.
	MetricInterval *string           `yaml:"metricInterval"`
	LogInterval    *string           `yaml:"logInterval"`
	TraceInterval  *string           `yaml:"traceInterval"`
	Resource       map[string]secret `yaml:"resource"`
}

type exporterTLSFile struct {
	CA   string `yaml:"ca"`
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// secret is a value written out, or read from { file: path } or
// { env: NAME }.
type secret struct {
	value string
	file  string
	env   string
	// written says the value is written out in the config.
	written bool
}

func (s *secret) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		s.value, s.written = node.Value, true
		return nil
	}
	var from struct {
		File string `yaml:"file"`
		Env  string `yaml:"env"`
	}
	if err := node.Decode(&from); err != nil || (from.File == "") == (from.Env == "") {
		return fmt.Errorf("%w: line %d: a value is written out, or read from { file: path } or { env: NAME }", ErrInvalid, node.Line)
	}
	s.file, s.env = from.File, from.Env
	return nil
}

// pairFile names a certificate file and its key file.
type pairFile struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// apiRef is an api an integration names: just its name, or
// { api: name, vendorTimeout: 5s } to change how long its calls wait.
type apiRef struct {
	name          string
	vendorTimeout *string
	// unknown are the lines of settings an integration may not change.
	unknown []string
}

func (r *apiRef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		r.name = node.Value
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%w: line %d: an api is named by its name or by { api: name }", ErrInvalid, node.Line)
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		switch key.Value {
		case "api":
			r.name = value.Value
		case "vendorTimeout":
			r.vendorTimeout = &value.Value
		default:
			r.unknown = append(r.unknown, fmt.Sprintf("line %d: field %s", key.Line, key.Value))
		}
	}
	return nil
}

// settingsFile holds the settings an api or the not-passed entry may set.
type settingsFile struct {
	TLSOrigination   *bool   `yaml:"tlsOrigination"`
	VendorTimeout    *string `yaml:"vendorTimeout"`
	AllowInternalIPs *bool   `yaml:"allowInternalIPs"`
}

const notPassed = "not-passed"

// Load reads the config file and every spec and certificate it points to.
// Their paths are relative to the folder of the config file. A certificate
// that has expired by now is refused.
//
// A refused config comes back with only Files set, next to the problems.
func Load(path string, now time.Time, lookupEnv func(string) (string, bool)) (*Config, error) {
	info, _ := os.Stat(path)
	tried := &Config{Files: map[string]os.FileInfo{path: info}}
	data, err := os.ReadFile(path) //nolint:gosec // the operator chooses the config file
	if err != nil {
		return tried, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return tried, fmt.Errorf("%w: %s is not valid YAML: %w", ErrInvalid, path, err)
	}
	var warnings []string
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	var unknown *yaml.TypeError
	if errors.As(strict.Decode(&file{}), &unknown) {
		for _, e := range unknown.Errors {
			field, _, _ := strings.Cut(e, " not found")
			warnings = append(warnings, field+" is not a setting and is ignored")
		}
	}
	l := loader{
		dir:       filepath.Dir(path),
		specs:     map[string]*spec.Spec{},
		files:     map[string][]byte{},
		lookupEnv: lookupEnv,
		read:      map[string]os.FileInfo{path: info},
		hashed:    map[string][]byte{path: data},
	}
	c := &Config{
		Name:         filepath.Base(path),
		Warnings:     warnings,
		NotPassed:    Settings{TLSOrigination: true, VendorTimeout: time.Minute},
		integrations: map[string]*Integration{},
		probes:       map[string]Probe{},
	}
	// Each integration's own settings, as the config writes them; the
	// not-passed entry's fill the gaps once every entry is read.
	traces := map[string]Trace{}
	urlFull := map[string]bool{}
	content := map[string]Content{}
	byID := map[string]map[string]*API{}
	if f.Listen.Kind != 0 {
		l.warnings = append(l.warnings, "listen is the old way to write ports and is ignored; write each port under listeners")
	}
	if f.Issuers.Kind != 0 {
		l.warnings = append(l.warnings, "issuers is now called certs and is ignored")
	}
	c.Certs = l.certs(f.Certs, now)
	c.Admin = l.admin(f.Admin, c.Certs)
	c.Listeners = l.listeners(f.Listeners, c.Certs)
	l.sameAddresses(c.Admin, c.Listeners)
	if len(c.Listeners) == 0 {
		l.problem("there are no listeners, so the proxy would take no calls")
	}
	if len(f.Exporters) == 0 {
		l.problem("there are no exporters")
	}
	for _, e := range f.Exporters {
		console := e.Console.Kind != 0
		switch kinds := btoi(e.OTLP != nil) + btoi(e.File != nil) + btoi(console); {
		case kinds > 1:
			l.problem("an exporter is only one of otlp, file or console")
		case e.OTLP != nil:
			c.Exporters = append(c.Exporters, l.exporter(*e.OTLP, now))
		case e.File != nil:
			c.FileExporters = append(c.FileExporters, l.fileExporter(*e.File, c.FileExporters))
		case console:
			c.Console = l.console(e.Console, c.Console)
		default:
			l.problem("an exporter must be otlp, file or console")
		}
	}
	c.Telemetry = l.telemetry(f.Telemetry, now)
	if f.Telemetry != nil && f.Telemetry.Pprof {
		c.Pprof = true
		if f.Admin == nil {
			l.warnings = append(l.warnings, "telemetry.pprof is on but there is no admin address, so there are no profile pages")
		}
	}
	c.CheckQueueMemory = l.size("specChecks.queueMemory", f.SpecChecks.QueueMemory, 64<<20)
	apis := map[string]*API{}
	hosts := map[string][]string{}
	for _, name := range slices.Sorted(maps.Keys(f.APIs)) {
		a := f.APIs[name]
		label := "api " + name
		if a.Spec == "" {
			l.problem("%s has no spec", label)
		}
		apis[name] = &API{
			Checks:   l.checks(label, a.Checks),
			Settings: l.settings(label, a.Settings, Settings{TLSOrigination: true, VendorTimeout: time.Minute}),
			Spec:     l.spec(label, a.Spec, a.Overlays),
		}
		probe := l.probe(label, a.Probe)
		for _, host := range a.Hosts {
			host = strings.ToLower(host)
			hosts[name] = append(hosts[name], host)
			if other, seen := c.probes[host]; seen {
				c.probes[host] = carefuller(probe, other)
				continue
			}
			c.probes[host] = probe
		}
	}
	for _, in := range f.Integrations {
		if in.ID == notPassed && len(in.APIs) > 0 {
			c.Warnings = append(c.Warnings, "the not-passed entry applies to every host, so its apis list is ignored")
		}
		if in.ID == "" {
			l.problem("an integration has no id")
			continue
		}
		t := Trace{Format: tracectx.W3C, Forward: in.TraceForward}
		if in.TraceFormat != nil {
			format, known := tracectx.Parse(*in.TraceFormat)
			if !known {
				l.problem("integration %s: traceFormat %s must be w3c, b3, xray or jaeger", in.ID, *in.TraceFormat)
			}
			t.Format = format
		}
		traces[in.ID] = t
		if in.URLFull != nil {
			urlFull[in.ID] = *in.URLFull
		}
		if kept, set := l.content("integration "+in.ID, in.Content, in.MaxBodySize); set {
			if kept.Full && len(c.FileExporters) == 0 {
				c.Warnings = append(c.Warnings, "integration "+in.ID+" has content: full but there is no file exporter, so its calls are not kept in full")
				kept.Full = false
			}
			if kept.Full {
				c.Warnings = append(c.Warnings, "integration "+in.ID+" has content: full, so file exporters get its calls with every header and whole bodies, passwords and personal data included")
			}
			content[in.ID] = kept
		}
		if in.ID == notPassed {
			c.NotPassed = l.settings("integration "+in.ID, in.Settings, c.NotPassed)
			continue
		}
		if _, seen := byID[in.ID]; seen {
			l.problem("two integrations have the ID %s", in.ID)
		}
		byHost := map[string]*API{}
		for _, ref := range in.APIs {
			for _, u := range ref.unknown {
				c.Warnings = append(c.Warnings, u+" is not a setting an integration may change, and is ignored")
			}
			api, known := apis[ref.name]
			if !known {
				l.problem("integration %s names the api %s, which is not in apis", in.ID, ref.name)
				continue
			}
			// Each integration gets its own copy, with its own settings.
			changed := *api
			if ref.vendorTimeout != nil {
				changed.Settings = l.settings("integration "+in.ID, settingsFile{VendorTimeout: ref.vendorTimeout}, api.Settings)
			}
			api = &changed
			for _, host := range hosts[ref.name] {
				if _, seen := byHost[host]; seen {
					l.problem("integration %s names two apis with the host %s", in.ID, host)
				}
				byHost[host] = api
			}
		}
		byID[in.ID] = byHost
	}
	// Each integration takes the not-passed entry's urlFull and content
	// where it says nothing, wherever the entry is in the file.
	settle := func(id string, known bool, apis map[string]*API, t Trace) *Integration {
		in := &Integration{Name: id, Known: known, APIs: apis, Trace: t}
		kept, set := content[id]
		if !set {
			kept = content[notPassed]
		}
		in.Content = kept
		on, set := urlFull[id]
		if !set {
			on = urlFull[notPassed]
		}
		in.URLFull = on || in.Content.Full
		return in
	}
	for id, apis := range byID {
		c.integrations[id] = settle(id, true, apis, traces[id])
	}
	// An unknown ID traces as the not-passed entry says, w3c otherwise.
	t, set := traces[notPassed]
	if !set {
		t = Trace{Format: tracectx.W3C}
	}
	c.notPassed = settle(notPassed, false, nil, t)
	c.Ledger = l.ledger(f)
	c.Files = l.read
	c.Warnings = append(c.Warnings, l.warnings...)
	if len(l.problems) > 0 {
		return &Config{Files: c.Files}, errors.Join(l.problems...)
	}
	c.Digest = l.digest()
	c.Fingerprint = strings.TrimPrefix(c.Digest, "sha256:")[:12]
	return c, nil
}

// ledger reads the ledger section: a batch every 10 seconds or at 65,536
// records unless set (Peter, 2026-11-16).
func (l *loader) ledger(f file) Ledger {
	out := Ledger{
		Every:      l.interval("ledger.batch", "every", f.Ledger.Batch.Every, 10*time.Second),
		MaxRecords: 65_536,
	}
	if n := f.Ledger.Batch.MaxRecords; n != nil {
		if *n < 1 {
			l.problem("ledger.batch: maxRecords must be at least 1")
		}
		out.MaxRecords = *n
	}
	if f.Ledger.Instance != nil {
		out.Instance = l.secret("ledger: instance", *f.Ledger.Instance)
	}
	return out
}

// digest hashes the config, specs and overlays in the order of their
// paths, each path relative to the config's folder, so the same files give
// the same digest wherever the folder is. Secret files are left out.
func (l *loader) digest() string {
	h := sha256.New()
	for _, path := range slices.Sorted(maps.Keys(l.hashed)) {
		name, _ := filepath.Rel(l.dir, path) // both are under the same root
		data := l.hashed[path]
		h.Write([]byte(name))
		h.Write([]byte{0})
		_ = binary.Write(h, binary.BigEndian, uint64(len(data)))
		h.Write(data)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

type loader struct {
	dir string
	// specs are the specs read so far, by their file and overlays, and
	// files the spec files read so far, so each is read once.
	specs     map[string]*spec.Spec
	files     map[string][]byte
	warnings  []string
	lookupEnv func(string) (string, bool)
	problems  []error
	// read are the files the load read or tried to read, as they were
	// just before, and hashed those the fingerprint covers, with what they
	// held.
	read   map[string]os.FileInfo
	hashed map[string][]byte
}

func (l *loader) exporter(f exporterFile, now time.Time) Exporter {
	label := "exporter " + f.Endpoint
	e := Exporter{
		Endpoint:      f.Endpoint,
		Gzip:          true,
		QueueSize:     10_000,
		Headers:       map[string]string{},
		LogInterval:   l.interval(label, "logInterval", f.LogInterval, 15*time.Second),
		TraceInterval: l.interval(label, "traceInterval", f.TraceInterval, 15*time.Second),
	}
	e.URL = l.endpoint(label, f.Endpoint)
	switch f.Compression {
	case "", "gzip":
	case "none":
		e.Gzip = false
	default:
		l.problem("%s: compression must be gzip or none", label)
	}
	switch f.H2C {
	case "", "false":
	case "true":
		if e.URL != nil && e.URL.Scheme != "http" {
			l.problem("%s: h2c needs a plain http:// endpoint", label)
		}
		e.H2C = true
	default:
		l.problem("%s: h2c must be true or false", label)
	}
	if f.QueueSize != nil {
		e.QueueSize = *f.QueueSize
	}
	if e.QueueSize < 1 {
		l.problem("%s: queueSize must be at least 1", label)
	}
	for _, name := range slices.Sorted(maps.Keys(f.Headers)) {
		e.Headers[name] = l.secret(label+": header "+name, f.Headers[name])
	}
	for _, name := range slices.Sorted(maps.Keys(f.Resource)) {
		switch {
		case name == "service.version":
			l.problem("%s: resource: service.version is the proxy's own and cannot be set", label)
		case !resourceName.MatchString(name):
			l.problem("%s: resource: the name %q may only hold letters, digits, dots and underscores", label, name)
		default:
			if e.Resource == nil {
				e.Resource = map[string]string{}
			}
			e.Resource[name] = l.secret(label+": resource "+name, f.Resource[name])
		}
	}
	if t := f.TLS; t != nil {
		if e.URL != nil && e.URL.Scheme != "https" {
			l.problem("%s: tls needs an https:// endpoint", label)
		}
		if t.CA != "" {
			e.CAs = l.authorities(label+": tls.ca", t.CA)
		}
		if (t.Cert == "") != (t.Key == "") {
			l.problem("%s: tls needs both cert and key, or neither", label)
		} else if t.Cert != "" {
			cert, err := loadPair(label+": tls", l.path(t.Cert), l.path(t.Key), now)
			if err != nil {
				l.problems = append(l.problems, err)
			}
			e.Certificate = cert
		}
	}
	return e
}

// resourceName is what a resource label's name may hold.
var resourceName = regexp.MustCompile(`^[A-Za-z0-9_.]+$`)

// endpoint reads where an exporter sends: a URL, whose https:// turns TLS
// on.
func (l *loader) endpoint(label, endpoint string) *url.URL {
	if !strings.Contains(endpoint, "://") {
		l.problem("%s: the endpoint must start with http:// or https://", label)
		return nil
	}
	u, err := url.Parse(endpoint)
	switch {
	case err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https":
		l.problem("%s: endpoint %s is not a valid address", label, endpoint)
	case u.User != nil:
		l.problem("%s: the endpoint may not hold a user name or password; use headers", label)
	case u.RawQuery != "" || u.Fragment != "":
		l.problem("%s: the endpoint may not have a query", label)
	default:
		return u
	}
	return nil
}

// secret reads a value without ever putting it into a message.
func (l *loader) secret(label string, s secret) string {
	switch {
	case s.written:
		return s.value
	case s.file != "":
		l.noteRead(l.path(s.file))
		data, err := os.ReadFile(l.path(s.file))
		if err != nil {
			l.problem("%s: the file %s cannot be read", label, s.file)
		}
		// Secret files often end in a line break, which is not part of
		// the value.
		return strings.TrimRight(string(data), "\r\n")
	}
	value, set := l.lookupEnv(s.env)
	if !set {
		l.problem("%s: the environment variable %s is not set", label, s.env)
	}
	return value
}

// certificate authorities reads a PEM file of certificates.
func (l *loader) authorities(label, path string) []byte {
	l.noteRead(l.path(path))
	data, err := os.ReadFile(l.path(path))
	if err != nil {
		l.problem("%s: %s cannot be read: %v", label, path, err)
		return nil
	}
	if !x509.NewCertPool().AppendCertsFromPEM(data) {
		l.problem("%s: %s holds no PEM certificate", label, path)
	}
	return data
}

func (l *loader) problem(format string, args ...any) {
	l.problems = append(l.problems, fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...)))
}

// interval reads one of an exporter's intervals, or gives the default.
func (l *loader) interval(label, name string, value *string, def time.Duration) time.Duration {
	if value == nil {
		return def
	}
	d, err := time.ParseDuration(*value)
	if err != nil || d <= 0 {
		l.problem("%s: %s %q must be a duration above zero, such as 15s", label, name, *value)
		return def
	}
	return d
}

func (l *loader) settings(label string, f settingsFile, s Settings) Settings {
	if f.TLSOrigination != nil {
		s.TLSOrigination = *f.TLSOrigination
	}
	if f.VendorTimeout != nil {
		d, err := time.ParseDuration(*f.VendorTimeout)
		if err != nil || d <= 0 {
			l.problem("%s: vendorTimeout %q must be a duration above zero, such as 10s", label, *f.VendorTimeout)
		}
		s.VendorTimeout = d
	}
	if f.AllowInternalIPs != nil {
		s.AllowInternalIPs = *f.AllowInternalIPs
	}
	return s
}

// passthrough reads the sites an ingress relays untouched: host names, or
// "*." and a suffix.
func (l *loader) passthrough(label string, names []string) Passthrough {
	var p Passthrough
	for _, name := range names {
		if validPattern(name, true) {
			p = append(p, strings.ToLower(name))
		} else {
			l.problem("%s: passthrough %q must be a host name or %q and a suffix", label, name, "*.")
		}
	}
	return p
}

// validPattern says whether name is a host name, or with wild set also "*."
// and a suffix.
func validPattern(name string, wild bool) bool {
	if wild {
		name = strings.TrimPrefix(name, "*.")
	}
	return name != "" && !strings.ContainsAny(name, "*/: ") && !strings.HasPrefix(name, ".")
}

func (l *loader) checks(label string, f *[]string) Parts {
	if f == nil {
		return AllParts
	}
	var p Parts
	for _, name := range *f {
		part, known := partNamed(name)
		if !known {
			l.problem("%s: unknown check %q; the checks are path, query, headers, cookies, body, status, response-headers, response-body and messages", label, name)
		}
		p |= part
	}
	return p
}

func (l *loader) pair(label string, f *pairFile, load func(certFile, keyFile string, now time.Time) (*tls.Certificate, error), now time.Time) *TLS {
	if f.Cert == "" || f.Key == "" {
		l.problem("%s needs both cert and key", label)
		return nil
	}
	t := &TLS{CertFile: l.path(f.Cert), KeyFile: l.path(f.Key)}
	l.noteRead(t.CertFile)
	l.noteRead(t.KeyFile)
	cert, err := load(t.CertFile, t.KeyFile, now)
	if err != nil {
		l.problems = append(l.problems, err)
	}
	t.Certificate = cert
	return t
}

func loadPair(label, certFile, keyFile string, now time.Time) (*tls.Certificate, error) {
	certPEM, certErr := os.ReadFile(certFile) //nolint:gosec // the config names its certificate files
	keyPEM, keyErr := os.ReadFile(keyFile)    //nolint:gosec // the config names its certificate files
	if err := errors.Join(certErr, keyErr); err != nil {
		return nil, fmt.Errorf("%w: %s: the cert or key file cannot be read: %w", ErrInvalid, label, err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %s and %s are not a matching certificate and key: %w", ErrInvalid, label, certFile, keyFile, err)
	}
	if notAfter := cert.Leaf.NotAfter; now.After(notAfter) {
		return nil, fmt.Errorf("%w: %s: the certificate in %s expired on %s", ErrInvalid, label, certFile, notAfter.UTC().Format(time.RFC3339))
	}
	return &cert, nil
}

// noteRead notes a file as it is just before the load reads it, so a
// change made while the load runs is seen by the next check.
func (l *loader) noteRead(path string) {
	if _, noted := l.read[path]; !noted {
		l.read[path], _ = os.Stat(path)
	}
}

func (l *loader) path(path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(l.dir, path)
	}
	return path
}

// spec reads the spec at path and applies the overlays to it. Apis with the
// same spec and the same overlays in the same order share one copy.
func (l *loader) spec(label, path string, overlays []string) *spec.Spec {
	if path == "" {
		return nil
	}
	path = l.path(path)
	l.noteRead(path)
	names := []string{path}
	files := make([]overlay.File, 0, len(overlays))
	for _, o := range overlays {
		o = l.path(o)
		l.noteRead(o)
		names = append(names, o)
		files = append(files, overlay.File{Name: o})
	}
	name := strings.Join(names, " with ")
	if s, read := l.specs[name]; read {
		return s
	}
	data, read := l.files[path]
	if !read {
		var err error
		data, err = os.ReadFile(path) //nolint:gosec // the config names its spec files
		if err != nil {
			l.problem("%s: spec %s cannot be read: %v", label, path, err)
			return nil
		}
		l.files[path] = data
		l.hashed[path] = data
	}
	if len(files) > 0 {
		for i := range files {
			var err error
			files[i].Data, err = os.ReadFile(files[i].Name)
			if err != nil {
				l.problem("%s: overlay %s cannot be read: %v", label, files[i].Name, err)
				return nil
			}
			l.hashed[files[i].Name] = files[i].Data
		}
		merged, warnings, err := overlay.Apply(data, files)
		if err != nil {
			l.problem("%s: %v", label, err)
			return nil
		}
		for _, w := range warnings {
			l.warnings = append(l.warnings, label+": "+w)
		}
		data = merged
	}
	s, err := spec.Parse(name, data, santhosh.NewEngine)
	if err != nil {
		l.problems = append(l.problems, fmt.Errorf("%s: %w", label, err))
	}
	l.specs[name] = s
	return s
}

// size reads a number of bytes, written plainly or with KiB, MiB or GiB,
// such as 64MiB. An empty one is the default.
func (l *loader) size(label, text string, def int64) int64 {
	if text == "" {
		return def
	}
	number, unit := text, int64(1)
	for suffix, bytes := range map[string]int64{"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30} {
		if cut, found := strings.CutSuffix(text, suffix); found {
			number, unit = cut, bytes
		}
	}
	n, err := strconv.ParseInt(number, 10, 64)
	switch {
	case err != nil:
		l.problem("%s must be a size such as 64MiB, not %q", label, text)
	case n < 1:
		l.problem("%s must be at least 1 byte", label)
	}
	return n * unit
}

// maxSocketPath is the longest path a socket file may have: macOS keeps
// at most 104 bytes, the last one ending the path.
const maxSocketPath = 103
