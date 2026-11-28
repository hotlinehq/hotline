package proxy_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"go.uber.org/goleak"
)

// TestFeatures runs the Gherkin files in ./features against the proxy as the
// system under test. The run is strict: a step without a definition fails.
// End-to-end scenarios run on their own, and scenarios tagged @later wait
// for a later slice.
//
// HOTLINE_FEATURES narrows the run while working on one part, to save the
// minute and a half the whole suite takes: a feature file, or one scenario
// by its line, several separated by commas. For example
// HOTLINE_FEATURES=features/transparent.feature:179 go test -run TestFeatures .
// Empty, as in CI, runs every feature.
func TestFeatures(t *testing.T) {
	paths := []string{"features"}
	if only := os.Getenv("HOTLINE_FEATURES"); only != "" {
		paths = strings.Split(only, ",")
	}
	app := &exampleApp{dir: t.TempDir()}
	ports := newReservedPorts()
	suite := godog.TestSuite{
		Name:                "hotline-proxy",
		ScenarioInitializer: func(sc *godog.ScenarioContext) { initializeScenario(sc, app, ports) },
		Options: &godog.Options{
			Format:   "progress",
			Paths:    paths,
			Tags:     "~@e2e && ~@later",
			Strict:   true,
			TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("feature files failed")
	}
}

func initializeScenario(sc *godog.ScenarioContext, app *exampleApp, ports *reservedPorts) {
	s := newScenario(ports, app)
	var before goleak.Option
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		before = goleak.IgnoreCurrent()
		return ctx, s.open()
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		s.close()
		if err == nil {
			// Every goroutine the proxy started must be gone once it has
			// shut down.
			// net/http keeps a closed connection's goroutine for half a
			// second, to let the other side read the response; it is not ours.
			opts := []goleak.Option{before, goleak.IgnoreAnyFunction("net/http.(*conn).closeWriteAndWait")}
			err = goleak.Find(opts...)
			// A stop with no time left leaves the check of the large body
			// running to its end; a body check cannot be interrupted, and
			// under the race detector it takes seconds, longer than Find
			// waits. Keep looking for a while.
			for end := time.Now().Add(30 * time.Second); err != nil && s.proxy.stoppedEarly && time.Now().Before(end); {
				pause(100 * time.Millisecond)
				err = goleak.Find(opts...)
			}
		}
		if err != nil && s.proxy.stoppedEarly && !strings.Contains(err.Error(), "hotline-proxy.") {
			// A stop that ran out of time cuts off the exporters' sends,
			// as the program then exits; the rest must still be gone.
			err = nil
		}
		return ctx, err
	})
	for _, area := range s.areas() {
		area.AddSteps(sc)
	}
	decisionSteps(sc)
}

// scenario is every sut of one scenario, built once and wired together:
// the shared ones first, then each area with only the suts it uses.
type scenario struct {
	proxy             *proxySUT
	vendors           *vendorsSUT
	certs             *certsSUT
	app               *appSUT
	apis              *apisSUT
	otlp              *otlpSUT
	fileExport        *fileExportSUT
	console           *consoleSUT
	tlsIngress        *tlsIngressSUT
	aws               *awsSUT
	connect           *connectSUT
	telemetry         *telemetrySUT
	capture           *captureSUT
	transparent       *transparentSUT
	ledger            *ledgerSUT
	listeners         *listenersSUT
	forward           *forwardSUT
	specChecks        *specChecksSUT
	streamedResponses *streamedResponseSUT
	webSocket         *webSocketSUT
	probe             *probeSUT
	configFile        *configFileSUT
}

// area is a sut with steps of its own.
type area interface {
	AddSteps(sc *godog.ScenarioContext)
}

func newScenario(ports *reservedPorts, example *exampleApp) *scenario {
	s := &scenario{proxy: newProxySUT(ports)}
	s.vendors = newVendorsSUT(s.proxy)
	s.certs = newCertsSUT(s.proxy)
	s.app = newAppSUT(s.proxy, s.vendors)
	s.apis = newApisSUT(s.proxy, s.app, s.vendors)
	s.otlp = newOTLPSUT(s.proxy, s.app, s.vendors, s.apis)
	s.fileExport = newFileExportSUT(s.proxy, s.vendors, s.app, s.apis, s.otlp)
	s.console = newConsoleSUT(s.proxy, s.vendors, s.app)
	s.tlsIngress = newTLSIngressSUT(s.proxy, s.vendors, s.app, s.otlp)
	s.aws = newAWSSUT(s.proxy, s.vendors, s.app, s.apis, s.otlp, example)
	s.connect = newConnectSUT(s.proxy, s.vendors, s.certs, s.app, s.otlp, s.tlsIngress)
	s.telemetry = newTelemetrySUT(s.proxy, s.vendors, s.certs, s.app, s.otlp, s.tlsIngress, s.connect)
	s.capture = newCaptureSUT(s.proxy, s.vendors, s.app, s.otlp, s.connect)
	s.transparent = newTransparentSUT(s.proxy, s.vendors, s.certs, s.app, s.otlp, s.connect)
	s.ledger = newLedgerSUT(s.proxy, s.vendors, s.certs, s.app, s.apis, s.otlp, s.fileExport, s.telemetry)
	s.listeners = newListenersSUT(s.proxy, s.vendors, s.certs, s.app, s.tlsIngress, s.connect, s.telemetry, s.capture)
	s.forward = newForwardSUT(s.proxy, s.vendors, s.app, s.apis, s.otlp, s.tlsIngress)
	s.specChecks = newSpecChecksSUT(s.proxy, s.vendors, s.app, s.apis, s.otlp, s.fileExport, s.console, s.aws, s.telemetry)
	s.streamedResponses = newStreamedResponseSUT(s.proxy, s.vendors, s.app, s.apis, s.otlp, s.fileExport, s.console)
	s.webSocket = newWebSocketSUT(s.proxy, s.vendors, s.app, s.apis, s.otlp, s.console, s.connect, s.capture)
	s.probe = newProbeSUT(s.proxy, s.vendors, s.app, s.apis, s.otlp, s.fileExport, s.console, s.ledger)
	s.configFile = newConfigFileSUT(s.proxy, s.vendors, s.certs, s.app, s.apis, s.otlp, s.tlsIngress, s.connect,
		s.telemetry, s.capture, s.transparent, s.listeners)
	return s
}

// areas are the suts with steps of their own.
func (s *scenario) areas() []area {
	return []area{
		s.otlp, s.fileExport, s.console, s.tlsIngress, s.aws, s.connect, s.telemetry, s.capture, s.transparent,
		s.ledger, s.listeners, s.forward, s.specChecks, s.streamedResponses, s.webSocket, s.probe, s.configFile,
	}
}

// open makes what the scenario needs before its first step: its folder
// and the first OTLP receiver.
func (s *scenario) open() error {
	s.otlp.Open()
	return s.proxy.Open()
}

// close stops everything the scenario started. The app's connections close
// first, so the proxy does not wait for them as it stops; the vendors and
// receivers stop after the proxy, which may still send to them.
func (s *scenario) close() {
	s.streamedResponses.Close()
	s.app.Close()
	s.proxy.Close()
	s.vendors.Close()
	s.otlp.Close()
	s.telemetry.Close()
}

var errCheck = errors.New("check failed")

func failf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errCheck, fmt.Sprintf(format, args...))
}

// has checks that a record holds key with exactly value.
func has(record map[string]any, key string, value any) error {
	got, found := record[key]
	if !found {
		return failf("the record has no %s; it has %v", key, record)
	}
	if fmt.Sprint(got) != fmt.Sprint(value) {
		return failf("%s is %v, want %v", key, got, value)
	}
	return nil
}

func hasNo(record map[string]any, keys ...string) error {
	for _, key := range keys {
		if got, found := record[key]; found {
			return failf("the record has %s = %v, want none", key, got)
		}
	}
	return nil
}

func hasAll(record map[string]any, keys ...string) error {
	for _, key := range keys {
		if _, found := record[key]; !found {
			return failf("the record has no %s; it has %v", key, record)
		}
	}
	return nil
}
