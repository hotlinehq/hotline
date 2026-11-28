package proxy_test

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	proxy "hotline-proxy"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// proxySUT is the root of every scenario: the proxy, and the pieces every
// area shares, namely the reserved ports, the scenario's folder, the clock,
// the environment, the proxy's logs and its config file. The areas hand it
// their part of the config with addConfig, their start options with
// addOption, and what to do before each start with beforeEachStart, so the
// root never reads an area's own fields.
type proxySUT struct {
	// ports hands out the ports the proxy is given, shared by the whole test
	// run so none is handed out twice.
	ports *reservedPorts
	// dir is the scenario's folder, made by Open: the config file, its specs
	// and anything else the proxy reads.
	dir string
	// clock, when set, replaces the system clock, so waits end only when
	// the test lets time pass.
	clock *fakeClock
	// env is the proxy's environment, and processEnv the variables set in
	// the test process itself, unset with the scenario.
	env        map[string]string
	processEnv []string
	logs       *syncBuffer
	// checkWorkers is how many spec checks run at once; 0 is one per core.
	checkWorkers int
	// exportTimeout is how long one send may take; 2s unless set, so a slow
	// first connection under the race detector still gets through.
	exportTimeout time.Duration
	// topExtra is added to the config's top level.
	topExtra string
	// rawConfig, when set, replaces the generated config file.
	rawConfig  string
	configPath string
	// configName is the config file's name; empty means hotline-proxy.yaml.
	configName string
	// busy is the address another program already listens on.
	busy string
	// noProxyPort leaves the proxy listener out of the config.
	noProxyPort bool
	// problem is what a refused config's message must name.
	problem string
	// opened are listeners a step opened, closed with the scenario.
	opened []net.Listener

	running *proxy.Proxy
	addr    string
	// stopped, while the proxy stops, closes once it has; stopTime is the
	// time it has to stop, 5 seconds when not set.
	stopped  chan struct{}
	stopTime time.Duration
	startErr error
	// stoppedEarly says the proxy was stopped with no time left, which cuts
	// off the exporters' sends.
	stoppedEarly bool
	// runs counts the proxy's starts, so each run has its own instance id.
	runs int
	// picked are the addresses start picked itself, and wroteConfig says it
	// wrote the config from the scenario, so it may pick them again when
	// they are taken.
	picked      []*string
	wroteConfig bool

	// parts are the areas' parts of the config, by the place they go in.
	parts map[configPlace][]func() string
	// options are what the areas add to the proxy's start options, and
	// prepare what they do before each start.
	options []func(*proxy.Options)
	prepare []func()
}

func newProxySUT(ports *reservedPorts) *proxySUT {
	sut := &proxySUT{ports: ports, logs: &syncBuffer{}, parts: map[configPlace][]func() string{}}
	sut.addConfig(configListeners, sut.proxyListener)
	return sut
}

// Open makes the scenario's folder.
func (sut *proxySUT) Open() error {
	dir, err := os.MkdirTemp("", "hotline-proxy-test-")
	sut.dir = dir
	return err
}

// Close stops the proxy, with two seconds to do so, and removes what the
// scenario left behind.
func (sut *proxySUT) Close() {
	if sut.running != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = sut.running.Shutdown(ctx)
		cancel()
	}
	if sut.stopped != nil {
		<-sut.stopped
	}
	for _, l := range sut.opened {
		_ = l.Close()
	}
	for _, name := range sut.processEnv {
		_ = os.Unsetenv(name)
	}
	_ = os.RemoveAll(sut.dir)
}

// configPlace is a place of the config file that areas add lines to.
type configPlace int

// The places, in the order of the file.
const (
	// configAdmin is the admin section.
	configAdmin configPlace = iota
	// configCerts is the entries of the certs section; an entry may start
	// with the section's own "certs:" line, which is written once.
	configCerts
	// configListeners is the proxy port and the listeners a scenario wrote,
	// and configIngressListeners the capture and transparent listeners after
	// them.
	configListeners
	configIngressListeners
	// configProxyListener is a line of the proxy port's listener.
	configProxyListener
	configExporters
	configTelemetry
	// configLedger is lines of the ledger section.
	configLedger
	// configApis is the apis and the integrations.
	configApis
)

// addConfig adds an area's part to a place of the config. lines is read
// each time the config is written.
func (sut *proxySUT) addConfig(place configPlace, lines func() string) {
	sut.parts[place] = append(sut.parts[place], lines)
}

// addOption adds an area's part of the proxy's start options.
func (sut *proxySUT) addOption(option func(*proxy.Options)) {
	sut.options = append(sut.options, option)
}

// beforeEachStart runs prepare before each start of the proxy.
func (sut *proxySUT) beforeEachStart(prepare func()) {
	sut.prepare = append(sut.prepare, prepare)
}

// section is what the areas wrote for a place of the config.
func (sut *proxySUT) section(place configPlace) string {
	var b strings.Builder
	for _, lines := range sut.parts[place] {
		b.WriteString(lines())
	}
	return b.String()
}

// configText is the config file the scenario describes.
func (sut *proxySUT) configText() string {
	var b strings.Builder
	b.WriteString(sut.section(configAdmin))
	b.WriteString(sut.certsSection())
	b.WriteString("listeners:\n" + sut.section(configListeners) + sut.section(configIngressListeners))
	b.WriteString("exporters:\n" + sut.section(configExporters))
	b.WriteString(sut.section(configTelemetry))
	b.WriteString(sut.topSections())
	b.WriteString(sut.section(configApis))
	return b.String()
}

// certsSection is the certs section: the certificate authorities and
// certificates the areas named.
func (sut *proxySUT) certsSection() string {
	written := false
	var b strings.Builder
	for _, lines := range sut.parts[configCerts] {
		part := lines()
		written = written || part != ""
		b.WriteString(strings.TrimPrefix(part, "certs:\n"))
	}
	if !written {
		return ""
	}
	return "certs:\n" + b.String()
}

// proxyListener is the proxy port's listener, with the lines the areas add
// to it.
func (sut *proxySUT) proxyListener() string {
	if sut.noProxyPort {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  proxy:\n    address: %q\n", "tcp://"+sut.addr)
	for _, lines := range sut.parts[configProxyListener] {
		if part := lines(); part != "" {
			b.WriteString("  " + part)
		}
	}
	return b.String()
}

// topSections are the config's other top sections, with the lines the
// areas add to the ledger section put in it, or in one of its own.
func (sut *proxySUT) topSections() string {
	top := sut.topExtra
	ledger := sut.section(configLedger)
	if ledger == "" {
		return top
	}
	if strings.Contains(top, "ledger:\n") {
		return strings.Replace(top, "ledger:\n", "ledger:\n"+ledger, 1)
	}
	return top + "ledger:\n" + ledger
}

func (sut *proxySUT) writeFile(name, content string) string {
	path := filepath.Join(sut.dir, name)
	_ = os.MkdirAll(filepath.Dir(path), 0o750)
	_ = os.WriteFile(path, []byte(content), 0o600) //nolint:gosec // the test chooses its own files
	return path
}

func (sut *proxySUT) exampleFile(name string) string {
	data, _ := os.ReadFile(filepath.Join("testdata", "proxy-example", name)) //nolint:gosec // test data
	return string(data)
}

// start writes the config and starts the proxy, if not done yet.
//
// A port found free a moment ago can be taken by another program on a busy
// computer before the proxy binds it, and so can a port the proxy let go of
// when a step stopped it to start it again. When that happens to a port the
// scenario picked itself, start picks new ports and tries again, up to three
// times. A port a scenario took on purpose is left alone, so the proxy still
// refuses it.
func (sut *proxySUT) start() error {
	if sut.running != nil || sut.startErr != nil {
		return sut.startErr
	}
	for try := 1; ; try++ {
		if err := sut.startOnce(); err == nil || try == 3 || !sut.mayPickAgain(err) {
			return err
		}
		sut.pickAgain()
	}
}

// mayPickAgain says the proxy could not bind an address start picked
// itself, in a config it wrote, so new ones may be picked.
func (sut *proxySUT) mayPickAgain(err error) bool {
	return strings.Contains(err.Error(), "address already in use") &&
		sut.busy == "" && sut.wroteConfig && len(sut.picked) > 0
}

// pickAgain forgets the addresses start picked and the config it wrote, so
// the next start picks new ones.
func (sut *proxySUT) pickAgain() {
	for _, address := range sut.picked {
		*address = ""
	}
	sut.picked = nil
	sut.configPath = ""
	sut.startErr = nil
}

// pick takes a free port for an address left empty, and remembers that
// start picked it.
func (sut *proxySUT) pick(address *string) {
	if *address == "" {
		*address = sut.ports.take()
		sut.picked = append(sut.picked, address)
	}
}

func (sut *proxySUT) startOnce() error {
	sut.pick(&sut.addr)
	for _, prepare := range sut.prepare {
		prepare()
	}
	if sut.configPath == "" {
		content := sut.rawConfig
		if content == "" {
			content = sut.configText()
			sut.wroteConfig = true
		}
		name := sut.configName
		if name == "" {
			name = "hotline-proxy.yaml"
		}
		sut.configPath = sut.writeFile(name, content)
	}
	if sut.exportTimeout == 0 {
		sut.exportTimeout = 2 * time.Second
	}
	var clock proxy.Clock = proxy.SystemClock{}
	if sut.clock != nil {
		clock = sut.clock
	}
	options := proxy.Options{
		ConfigPath:    sut.configPath,
		Clock:         clock,
		Logger:        slog.New(slog.NewTextHandler(sut.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		ExportTimeout: sut.exportTimeout,
		CheckWorkers:  sut.checkWorkers,
		Version:       "1.0.0-test",
		InstanceID:    fmt.Sprintf("proxy-test-%d", sut.runs+1),
		LookupEnv: func(name string) (string, bool) {
			v, ok := sut.env[name]
			return v, ok
		},
	}
	for _, add := range sut.options {
		add(&options)
	}
	sut.running, sut.startErr = proxy.Start(context.Background(), options)
	if sut.startErr == nil {
		sut.runs++
	}
	return sut.startErr
}

// settle stops the proxy, which sends every queued record and count, and
// waits until it has stopped.
func (sut *proxySUT) settle() {
	sut.startStop()
	if sut.stopped != nil {
		<-sut.stopped
		sut.stopped = nil
	}
}

// startStop starts the proxy's stop, which runs on its own: open
// connections still pass bytes until they end, or until the time to stop
// runs out.
func (sut *proxySUT) startStop() {
	if sut.running == nil {
		return
	}
	p, stopped := sut.running, make(chan struct{})
	sut.running, sut.stopped = nil, stopped
	limit := cmp.Or(sut.stopTime, 5*time.Second)
	go func() {
		defer close(stopped)
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()
		_ = p.Shutdown(ctx)
	}()
}

// startStopping starts the proxy's stop and waits, at most 2 seconds,
// until its port takes no new connection, so what the scenario does next
// happens while the proxy stops.
func (sut *proxySUT) startStopping() error {
	addr := sut.addr
	sut.startStop()
	if !stopsWithin(addr, 2*time.Second) {
		return failf("the proxy still takes connections 2 seconds into its stop")
	}
	return nil
}

// stopsWithin says whether addr stops taking new connections within limit.
func stopsWithin(addr string, limit time.Duration) bool {
	return eventually(limit, func() bool { return !accepts(addr) })
}

// accepts says whether addr takes a new connection.
func accepts(addr string) bool {
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// syncBuffer collects the proxy's own log lines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

func sprint(v any) string { return fmt.Sprint(v) }

func mustURL(raw string) *url.URL {
	u, _ := url.Parse(raw)
	return u
}
