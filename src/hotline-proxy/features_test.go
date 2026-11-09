package proxy_test

import (
	"context"
	"errors"
	"fmt"
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
func TestFeatures(t *testing.T) {
	app := &exampleApp{dir: t.TempDir()}
	suite := godog.TestSuite{
		Name:                "hotline-proxy",
		ScenarioInitializer: func(sc *godog.ScenarioContext) { initializeScenario(sc, app) },
		Options: &godog.Options{
			Format:   "progress",
			Paths:    []string{"features"},
			Tags:     "~@e2e && ~@later",
			Strict:   true,
			TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("feature files failed")
	}
}

func initializeScenario(sc *godog.ScenarioContext, app *exampleApp) {
	w := &world{}
	var before goleak.Option
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		before = goleak.IgnoreCurrent()
		fresh, err := newWorld()
		*w = *fresh
		return ctx, err
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		w.close()
		if err == nil {
			// Every goroutine the proxy started must be gone once it has
			// shut down.
			// net/http keeps a closed connection's goroutine for half a
			// second, to let the other side read the reply; it is not ours.
			opts := []goleak.Option{before, goleak.IgnoreAnyFunction("net/http.(*conn).closeWriteAndWait")}
			err = goleak.Find(opts...)
			// A stop with no time left leaves the check of the large body
			// running to its end; a body check cannot be interrupted, and
			// under the race detector it takes seconds, longer than Find
			// waits. Keep looking for a while.
			for end := time.Now().Add(30 * time.Second); err != nil && w.stoppedEarly && time.Now().Before(end); {
				time.Sleep(100 * time.Millisecond)
				err = goleak.Find(opts...)
			}
		}
		if err != nil && w.stoppedEarly && !strings.Contains(err.Error(), "hotline-proxy.") {
			// A stop that ran out of time cuts off the exporters' sends,
			// as the program then exits; the rest must still be gone.
			err = nil
		}
		return ctx, err
	})
	configSteps(sc, w)
	checksSteps(sc, w)
	specSteps(sc, w)
	ingressSteps(sc, w)
	integrationSteps(sc, w)
	forwardSteps(sc, w)
	trailerSteps(sc, w)
	timingSteps(sc, w)
	redactionSteps(sc, w)
	headerSteps(sc, w)
	recordSteps(sc, w)
	exportSteps(sc, w)
	signatureSteps(sc, w)
	bodyFieldSteps(sc, w)
	protocolSteps(sc, w)
	tlsSteps(sc, w)
	guardSteps(sc, w)
	hostHeaderSteps(sc, w)
	connectSteps(sc, w)
	interceptSteps(sc, w)
	overlaySteps(sc, w)
	awsSteps(sc, w)
	shopSteps(sc, w)
	xmlSteps(sc, w)
	routeSteps(sc, w)
	bodyRouteSteps(sc, w)
	s3Steps(sc, w, app)
	traceSteps(sc, w)
	reloadSteps(sc, w)
	telemetrySteps(sc, w)
	memorySteps(sc, w)
	specLineSteps(sc, w)
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
