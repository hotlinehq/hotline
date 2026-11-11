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
	ProxyAddr string
	// AdminAddr is where the health checks listen; empty means none.
	AdminAddr string
	// TLS, when set, makes the proxy port accept only TLS, with this
	// certificate.
	TLS *TLS
	// Connect, when set, makes CONNECT an ingress: this issuer signs the
	// certificate the proxy shows for each vendor host inside a tunnel.
	Connect *TLS
	// ConnectPassthrough lists the sites a tunnel relays untouched, from
	// listen.connect.
	ConnectPassthrough Passthrough
	// Intercept, when set, makes a port of its own an ingress for
	// connections a firewall redirected to the proxy.
	Intercept *Intercept
	// Issuers are the named issuers of the issuers section, each checked
	// to be an issuer, used or not.
	Issuers   map[string]*TLS
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
	// Files are every file the load read or tried to read, so the proxy
	// can tell when one changes. A refused config still lists them.
	Files []string
	// Warnings name settings the proxy does not use, such as a misspelt
	// field. They do not stop the proxy.
	Warnings     []string
	integrations map[string]map[string]*API
	// traces are each integration's trace settings, the not-passed entry's
	// among them.
	traces map[string]Trace
	// urlFull is, for each integration, whether its minimal records carry
	// url.full; the not-passed entry's value is the default.
	urlFull map[string]bool
	// content is, for each integration, whether its calls are kept in full
	// for the file exporters; the not-passed entry's value is the default.
	content map[string]Content
}

// Content says whether an integration's calls are kept in full, with every
// header and their bodies, for the file exporters, and how much of each
// body is kept.
type Content struct {
	Full    bool
	MaxBody int64
}

// Content is an integration's content setting, or the not-passed entry's
// for those that say nothing. It is never full without a file exporter,
// since the copy would be kept for nothing (Peter, 2026-11-11).
func (c *Config) Content(integrationID string) Content {
	if on, ok := c.content[integrationID]; ok {
		return on
	}
	return c.content[notPassed]
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

// URLFull says whether a minimal record of this integration's call carries
// url.full, the address as sent. It is off unless the integration, or the
// not-passed entry for those that say nothing, switches it on (Peter,
// 2026-11-08).
func (c *Config) URLFull(integrationID string) bool {
	if on, ok := c.urlFull[integrationID]; ok {
		return on
	}
	return c.urlFull[notPassed]
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

// TLS is a certificate the proxy shows apps, or the issuer it signs them
// with: the files it comes from, and the pair they held when the config
// was loaded.
type TLS struct {
	CertFile    string
	KeyFile     string
	Certificate *tls.Certificate
}

// Exporter is one OTLP destination.
type Exporter struct {
	// Endpoint is the destination as the config names it.
	Endpoint string
	// URL is where to send. Its scheme is https for TLS, else http. Its
	// path goes in front of /v1/logs, /v1/metrics and /v1/traces.
	URL *url.URL
	// CAs are PEM certificates of issuers to trust on top of the proxy's
	// own, for TLS.
	CAs []byte
	// Certificate, when set, is shown to the destination in mutual TLS.
	Certificate *tls.Certificate
	// Headers go with every request, login headers among them.
	Headers map[string]string
	Gzip    bool
	// H2C sends HTTP/2 from the first byte to a plain http:// endpoint
	// (Peter, 2026-11-08).
	H2C   bool
	Delta bool
	// QueueSize is how many records wait for the destination at most.
	QueueSize int
	// MetricInterval, LogInterval and TraceInterval are how often counts,
	// records and spans are sent. Records and spans also go sooner once a
	// batch of them waits.
	MetricInterval time.Duration
	LogInterval    time.Duration
	TraceInterval  time.Duration
	// Resource are labels that describe where the proxy runs, sent to this
	// destination only. They may change service.name and
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
	// SigningKey signs the integration's records: its fingerprintKey, or
	// its ID when it has none.
	SigningKey []byte
}

// Trace is the trace settings of an integration. An unknown ID gets the
// not-passed entry's, and those are w3c, not forwarded, unless it sets them.
func (c *Config) Trace(integrationID string) Trace {
	if t, ok := c.traces[integrationID]; ok {
		return t
	}
	if t, ok := c.traces[notPassed]; ok {
		return t
	}
	return Trace{Format: tracectx.W3C}
}

// API finds the api an integration uses for a host, or nil when the ID is
// unknown or the host is outside the integration's apis.
func (c *Config) API(integrationID, host string) *API {
	return c.integrations[integrationID][strings.ToLower(host)]
}

// file is the config file's layout.
type file struct {
	Listen struct {
		Proxy     string         `yaml:"proxy"`
		Admin     string         `yaml:"admin"`
		TLS       *pairFile      `yaml:"tls"`
		Connect   *connectFile   `yaml:"connect"`
		Intercept *interceptFile `yaml:"intercept"`
	} `yaml:"listen"`
	// Issuers are certificate authorities the proxy signs with, each
	// written once under a name that the ingresses pick.
	Issuers    map[string]*pairFile `yaml:"issuers"`
	Telemetry  *telemetryFile       `yaml:"telemetry"`
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
	} `yaml:"apis"`
	Exporters []struct {
		OTLP    *exporterFile        `yaml:"otlp"`
		File    *fileExporterFile    `yaml:"file"`
		Console *consoleExporterFile `yaml:"console"`
	} `yaml:"exporters"`
	Integrations []struct {
		ID             string       `yaml:"id"`
		FingerprintKey *secret      `yaml:"fingerprintKey"`
		Settings       settingsFile `yaml:",inline"`
		APIs           []apiRef     `yaml:"apis"`
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
}

// fileExporterFile is one file exporter as the config writes it.
type fileExporterFile struct {
	Path        string `yaml:"path"`
	MaxFileSize string `yaml:"maxFileSize"`
	MaxFiles    *int   `yaml:"maxFiles"`
	Compression string `yaml:"compression"`
	QueueMemory string `yaml:"queueMemory"`
}

// exporterFile is one OTLP destination as the config writes it.
type exporterFile struct {
	Endpoint    string            `yaml:"endpoint"`
	Headers     map[string]secret `yaml:"headers"`
	TLS         *exporterTLSFile  `yaml:"tls"`
	Compression string            `yaml:"compression"`
	// H2C is true or false, read as text so a wrong value is named.
	H2C         string `yaml:"h2c"`
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

// connectFile is listen.connect: the name of the issuer it signs with.
type connectFile struct {
	Issuer      string   `yaml:"issuer"`
	Passthrough []string `yaml:"passthrough"`
	// Cert and Key are the removed inline form, read only to refuse it.
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// interceptFile is listen.intercept.
type interceptFile struct {
	Address     string   `yaml:"address"`
	Issuer      string   `yaml:"issuer"`
	Integration string   `yaml:"integration"`
	Passthrough []string `yaml:"passthrough"`
	ECH         struct {
		Relay []string `yaml:"relay"`
	} `yaml:"ech"`
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
	tried := &Config{Files: []string{path}}
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
		read:      map[string]bool{path: true},
		hashed:    map[string][]byte{path: data},
	}
	c := &Config{
		Name:         filepath.Base(path),
		Warnings:     warnings,
		ProxyAddr:    f.Listen.Proxy,
		AdminAddr:    f.Listen.Admin,
		NotPassed:    Settings{TLSOrigination: true, VendorTimeout: time.Minute},
		integrations: map[string]map[string]*API{},
		traces:       map[string]Trace{},
		urlFull:      map[string]bool{},
		content:      map[string]Content{},
	}
	if f.Listen.Proxy == "" {
		l.problem("there is no listen.proxy address")
	}
	if f.Listen.Admin == "" {
		c.Warnings = append(c.Warnings, "there is no listen.admin address, so the proxy has no health checks")
	}
	if t := f.Listen.TLS; t != nil {
		c.TLS = l.pair("listen.tls", t, LoadCertificate, now)
	}
	c.Issuers = map[string]*TLS{}
	for _, name := range slices.Sorted(maps.Keys(f.Issuers)) {
		if f.Issuers[name] == nil {
			l.problem("issuer %s needs both cert and key", name)
			continue
		}
		c.Issuers[name] = l.pair("issuer "+name, f.Issuers[name], LoadIssuer, now)
	}
	if t := f.Listen.Connect; t != nil {
		switch {
		case t.Cert != "" || t.Key != "":
			l.problem("listen.connect must name an issuer, as { issuer: name } with the issuer in the issuers section; cert and key are no longer written in listen.connect")
		case t.Issuer == "":
			l.problem("listen.connect must name an issuer, as { issuer: name }")
		default:
			if _, known := f.Issuers[t.Issuer]; known {
				c.Connect = c.Issuers[t.Issuer]
			} else {
				l.problem("listen.connect names the issuer %s, which is not in issuers", t.Issuer)
			}
		}
		for _, name := range t.Passthrough {
			if validPattern(name, true) {
				c.ConnectPassthrough = append(c.ConnectPassthrough, strings.ToLower(name))
			} else {
				l.problem("listen.connect: passthrough %q must be a host name or %q and a suffix", name, "*.")
			}
		}
	}
	if t := f.Listen.Intercept; t != nil {
		c.Intercept = l.intercept(t, c.Issuers, f.Issuers)
	}
	if len(f.Exporters) == 0 {
		l.problem("there are no exporters")
	}
	for _, e := range f.Exporters {
		switch kinds := btoi(e.OTLP != nil) + btoi(e.File != nil) + btoi(e.Console != nil); {
		case kinds > 1:
			l.problem("an exporter is only one of otlp, file or console")
		case e.OTLP != nil:
			c.Exporters = append(c.Exporters, l.exporter(*e.OTLP, now))
		case e.File != nil:
			c.FileExporters = append(c.FileExporters, l.fileExporter(*e.File, c.FileExporters))
		case e.Console != nil:
			c.Console = l.console(*e.Console, c.Console)
		default:
			l.problem("an exporter must be otlp, file or console")
		}
	}
	c.Telemetry = l.telemetry(f.Telemetry, now)
	if f.Telemetry != nil && f.Telemetry.Pprof {
		c.Pprof = true
		if f.Listen.Admin == "" {
			c.Warnings = append(c.Warnings, "telemetry.pprof is on but there is no listen.admin address, so there are no profile pages")
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
		for _, host := range a.Hosts {
			hosts[name] = append(hosts[name], strings.ToLower(host))
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
		c.traces[in.ID] = t
		if in.URLFull != nil {
			c.urlFull[in.ID] = *in.URLFull
		}
		if content, set := l.content("integration "+in.ID, in.Content, in.MaxBodySize); set {
			if content.Full && len(c.FileExporters) == 0 {
				c.Warnings = append(c.Warnings, "integration "+in.ID+" has content: full but there is no file exporter, so its calls are not kept in full")
				content.Full = false
			}
			if content.Full {
				c.Warnings = append(c.Warnings, "integration "+in.ID+" has content: full, so file exporters get its calls with every header and whole bodies, passwords and personal data included")
			}
			c.content[in.ID] = content
		}
		if in.ID == notPassed {
			c.NotPassed = l.settings("integration "+in.ID, in.Settings, c.NotPassed)
			continue
		}
		if _, seen := c.integrations[in.ID]; seen {
			l.problem("two integrations have the ID %s", in.ID)
		}
		key := []byte(in.ID)
		switch {
		case in.FingerprintKey == nil:
			c.Warnings = append(c.Warnings, "integration "+in.ID+" has no fingerprintKey, so its records are signed with its ID, which is not secret")
		case in.FingerprintKey.written:
			l.problem("integration %s: fingerprintKey must be read from { file: path } or { env: NAME }, never written in the config", in.ID)
		default:
			key = []byte(l.secret("integration "+in.ID+": fingerprintKey", *in.FingerprintKey))
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
			// Each integration gets its own copy, with its own key.
			changed := *api
			changed.SigningKey = key
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
		c.integrations[in.ID] = byHost
	}
	c.Files = slices.Sorted(maps.Keys(l.read))
	if len(l.problems) > 0 {
		return &Config{Files: c.Files}, errors.Join(l.problems...)
	}
	c.Warnings = append(c.Warnings, l.warnings...)
	c.Fingerprint = l.fingerprint()
	return c, nil
}

// fingerprint hashes the config, specs and overlays in the order of their
// paths, each path relative to the config's folder, so the same files give
// the same fingerprint wherever the folder is.
func (l *loader) fingerprint() string {
	h := sha256.New()
	for _, path := range slices.Sorted(maps.Keys(l.hashed)) {
		name, _ := filepath.Rel(l.dir, path) // both are under the same root
		data := l.hashed[path]
		h.Write([]byte(name))
		h.Write([]byte{0})
		_ = binary.Write(h, binary.BigEndian, uint64(len(data)))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
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
	// read are the files the load read or tried to read, and hashed those
	// the fingerprint covers, with what they held.
	read   map[string]bool
	hashed map[string][]byte
}

func (l *loader) exporter(f exporterFile, now time.Time) Exporter {
	label := "exporter " + f.Endpoint
	e := Exporter{
		Endpoint:  f.Endpoint,
		Gzip:      true,
		Delta:     f.Temporality == "delta",
		QueueSize: 10_000,
		Headers:   map[string]string{},
		// A data point a minute is what Grafana Cloud's standard pricing
		// covers (Peter, 2026-11-01).
		MetricInterval: l.interval(label, "metricInterval", f.MetricInterval, time.Minute),
		LogInterval:    l.interval(label, "logInterval", f.LogInterval, 15*time.Second),
		TraceInterval:  l.interval(label, "traceInterval", f.TraceInterval, 15*time.Second),
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
	switch f.Temporality {
	case "", "cumulative", "delta":
	default:
		l.problem("%s: temporality must be cumulative or delta", label)
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
			e.CAs = l.issuers(label+": tls.ca", t.CA)
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
		l.read[l.path(s.file)] = true
		data, err := os.ReadFile(l.path(s.file)) //nolint:gosec // the config names its secret files
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

// issuers reads a PEM file of certificates.
func (l *loader) issuers(label, path string) []byte {
	l.read[l.path(path)] = true
	data, err := os.ReadFile(l.path(path)) //nolint:gosec // the config names its certificate files
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

func (l *loader) intercept(f *interceptFile, issuers map[string]*TLS, written map[string]*pairFile) *Intercept {
	i := &Intercept{Address: f.Address, Integration: f.Integration}
	if f.Address == "" {
		l.problem("listen.intercept needs an address")
	}
	switch _, known := written[f.Issuer]; {
	case f.Issuer == "":
		l.problem("listen.intercept must name an issuer, as issuer: name")
	case !known:
		l.problem("listen.intercept names the issuer %s, which is not in issuers", f.Issuer)
	default:
		i.Issuer = issuers[f.Issuer]
	}
	for _, name := range f.Passthrough {
		if validPattern(name, true) {
			i.Passthrough = append(i.Passthrough, strings.ToLower(name))
		} else {
			l.problem("listen.intercept: passthrough %q must be a host name or %q and a suffix", name, "*.")
		}
	}
	for _, name := range f.ECH.Relay {
		if validPattern(name, false) {
			i.ECHRelay = append(i.ECHRelay, strings.ToLower(name))
		} else {
			l.problem("listen.intercept: ech.relay %q must be a host name", name)
		}
	}
	return i
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
			l.problem("%s: unknown check %q; the checks are path, query, headers, cookies, body, status, reply-headers and reply-body", label, name)
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
	l.read[t.CertFile], l.read[t.KeyFile] = true, true
	cert, err := load(t.CertFile, t.KeyFile, now)
	if err != nil {
		l.problems = append(l.problems, err)
	}
	t.Certificate = cert
	return t
}

// LoadCertificate reads a PEM certificate, with any issuers in between, and
// its key, and checks that they match and that the certificate has not
// expired by now.
func LoadCertificate(certFile, keyFile string, now time.Time) (*tls.Certificate, error) {
	return loadPair("listen.tls", certFile, keyFile, now)
}

// LoadIssuer reads the issuer the proxy signs its certificates for vendor
// hosts with, as LoadCertificate does, and checks that it is an issuer:
// a certificate authority allowed to sign certificates.
func LoadIssuer(certFile, keyFile string, now time.Time) (*tls.Certificate, error) {
	cert, err := loadPair("issuer", certFile, keyFile, now)
	if err != nil {
		return nil, err
	}
	if leaf := cert.Leaf; !leaf.IsCA || leaf.KeyUsage != 0 && leaf.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, fmt.Errorf("%w: the certificate in %s is not an issuer: it may not sign certificates", ErrInvalid, certFile)
	}
	return cert, nil
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
	l.read[path] = true
	names := []string{path}
	files := make([]overlay.File, 0, len(overlays))
	for _, o := range overlays {
		o = l.path(o)
		l.read[o] = true
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
			files[i].Data, err = os.ReadFile(files[i].Name) //nolint:gosec // the config names its overlay files
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
