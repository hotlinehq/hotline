// Command hotline-proxy runs the Hotline proxy. It reads its command line,
// connects the proxy to the real network, clock and log, starts it, and
// stops it on SIGTERM or SIGINT. All the proxy's logic lives in package
// proxy.
package main

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	proxy "hotline-proxy"
	"hotline-proxy/internal/overlay"
)

// Exit codes.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

const usage = `usage: hotline-proxy <command> [flags]

commands:
  run       start the proxy
  validate  check a config and the specs it points to, then exit
  overlay   write an overlay that turns one spec into another
  version   print the version

Run "hotline-proxy <command> --help" for the flags of a command.
`

var (
	errOutOfTime = errors.New("stop ran out of time")
	errCutShort  = errors.New("stop cut short by a second signal")
	errTrustFile = errors.New("trust file")
)

// checkEvery is how often the proxy looks at its files for a change.
const checkEvery = 5 * time.Second

func main() {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	reloads := make(chan os.Signal, 1)
	signal.Notify(reloads, syscall.SIGHUP)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, signals, reloads))
}

func run(args []string, stdout, stderr io.Writer, signals, reloads <-chan os.Signal) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return exitUsage
	}
	command := args[0]
	switch command {
	case "version":
		_, _ = fmt.Fprintln(stdout, "hotline-proxy", version())
		return exitOK
	case "overlay":
		return makeOverlay(args[1:], stdout, stderr)
	case "run", "validate":
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n\n%s", command, usage)
		return exitUsage
	}

	flags := flag.NewFlagSet("hotline-proxy "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "the hotline-proxy.yaml file (required)")
	trustFile := flags.String("trust-file", "", "a PEM file of extra trusted issuers for vendor certificates, on top of the system's own")
	level := slog.LevelInfo
	flags.TextVar(&level, "log-level", slog.LevelInfo, "the lowest level logged: debug, info, warn or error")
	shutdownTimeout := flags.Duration("shutdown-timeout", 25*time.Second, "how long a stop may take (run only)")
	drainDelay := flags.Duration("drain-delay", 5*time.Second, "how long new calls are still taken after the stop signal, while Kubernetes stops sending them; part of the shutdown timeout (run only)")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	switch {
	case flags.NArg() > 0:
		_, _ = fmt.Fprintf(stderr, "unexpected argument %q\n", flags.Arg(0))
		flags.Usage()
		return exitUsage
	case *configPath == "":
		_, _ = fmt.Fprintln(stderr, "--config is required")
		flags.Usage()
		return exitUsage
	}

	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: level}))
	roots, err := trustPool(*trustFile)
	if err != nil {
		logger.Error("trust file refused", "error", err.Error())
		return exitFail
	}
	if command == "validate" {
		warnings, err := proxy.Validate(*configPath, proxy.SystemClock{}, os.LookupEnv)
		if err != nil {
			logProblems(logger, "config refused", err)
			return exitFail
		}
		for _, w := range warnings {
			logger.Warn("config warning", "reason", w)
		}
		logger.Info("config is valid", "config", *configPath)
		return exitOK
	}
	return serve(logger, proxy.Options{
		ConfigPath:    *configPath,
		Resolver:      net.DefaultResolver,
		Dialer:        &net.Dialer{},
		RootCAs:       roots,
		Clock:         proxy.SystemClock{},
		Logger:        logger,
		ExportTimeout: 10 * time.Second,
		Version:       version(),
		InstanceID:    instanceID(),
		LookupEnv:     os.LookupEnv,

		OriginalDestination: originalDestination,
	}, stopping{timeout: *shutdownTimeout, drainDelay: *drainDelay}, signals, reloads)
}

// makeOverlay writes to stdout an overlay that turns the --from spec into
// the --to spec.
func makeOverlay(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hotline-proxy overlay", flag.ContinueOnError)
	flags.SetOutput(stderr)
	from := flags.String("from", "", "the spec as it is (required)")
	to := flags.String("to", "", "the spec as it should be (required)")
	title := flags.String("title", "Hotline overlay", "the overlay's title")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	switch {
	case flags.NArg() > 0:
		_, _ = fmt.Fprintf(stderr, "unexpected argument %q\n", flags.Arg(0))
		flags.Usage()
		return exitUsage
	case *from == "" || *to == "":
		_, _ = fmt.Fprintln(stderr, "--from and --to are required")
		flags.Usage()
		return exitUsage
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	files := [2]overlay.File{{Name: *from}, {Name: *to}}
	for i := range files {
		var err error
		files[i].Data, err = os.ReadFile(files[i].Name)
		if err != nil {
			logger.Error("overlay not made", "problem", fmt.Sprintf("%s cannot be read: %v", files[i].Name, err))
			return exitFail
		}
	}
	out, err := overlay.Make(*title, files[0], files[1])
	if err != nil {
		logger.Error("overlay not made", "problem", err.Error())
		return exitFail
	}
	_, _ = stdout.Write(out)
	return exitOK
}

// stopping is how the program stops: within timeout, still taking new
// calls for drainDelay first.
type stopping struct {
	timeout    time.Duration
	drainDelay time.Duration
}

// serve runs the proxy until the first signal, then stops it within the
// timeout. Until then it checks the config's files every few seconds, and
// SIGHUP reloads the config at once. For the drain delay it still takes
// new calls while /readyz says no, so Kubernetes has time to stop sending
// them. A second signal cuts the stop short.
func serve(logger *slog.Logger, o proxy.Options, stop stopping, signals, reloads <-chan os.Signal) int {
	p, err := proxy.Start(context.Background(), o)
	if err != nil {
		logProblems(logger, "hotline proxy did not start", err)
		return exitFail
	}
	sig := watch(p, signals, reloads)
	p.Drain()
	logger.Info("hotline proxy stopping", "signal", sig.String(), "timeout", stop.timeout.String(), "drain_delay", stop.drainDelay.String())
	ctx, cancel := context.WithTimeoutCause(context.Background(), stop.timeout, errOutOfTime)
	defer cancel()
	ctx, cutShort := context.WithCancelCause(ctx)
	defer cutShort(nil)
	go func() {
		select {
		case <-signals:
			cutShort(errCutShort)
		case <-ctx.Done():
		}
	}()
	drained := time.NewTimer(stop.drainDelay)
	defer drained.Stop()
	select {
	case <-drained.C:
	case <-ctx.Done():
	}
	if err := p.Shutdown(ctx); err != nil {
		logger.Error("hotline proxy stopped with calls or records left", "reason", fmt.Sprint(context.Cause(ctx)), "error", err.Error())
		return exitFail
	}
	logger.Info("hotline proxy stopped")
	return exitOK
}

// watch keeps the config up to date until a stop signal comes, and gives
// that signal.
func watch(p *proxy.Proxy, signals, reloads <-chan os.Signal) os.Signal {
	check := time.NewTicker(checkEvery)
	defer check.Stop()
	for {
		select {
		case sig := <-signals:
			return sig
		case <-reloads:
			p.Reload()
		case <-check.C:
			p.CheckFiles()
		}
	}
}

// trustPool is the system's trusted issuers plus those in the trust file,
// if there is one.
func trustPool(path string) (*x509.CertPool, error) {
	pool, poolErr := x509.SystemCertPool()
	if path == "" {
		return pool, poolErr
	}
	data, readErr := os.ReadFile(path) //nolint:gosec // the operator chooses the trust file
	if err := errors.Join(poolErr, readErr); err != nil {
		return nil, fmt.Errorf("%w %s: %w", errTrustFile, path, err)
	}
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("%w %s holds no certificate", errTrustFile, path)
	}
	return pool, nil
}

// logProblems logs one line per problem, so a config with many problems
// lists each of them.
func logProblems(logger *slog.Logger, msg string, err error) {
	problems := []error{err}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		problems = joined.Unwrap()
	}
	for _, problem := range problems {
		logger.Error(msg, "problem", problem.Error())
	}
}

// version is the module version Go records when it builds the program.
// buildVersion is the release's git tag, such as v0.1.0, set when the
// program is built: go build -ldflags "-X main.buildVersion=v0.1.0".
var buildVersion string //nolint:gochecknoglobals // go build -ldflags -X can only set a package variable

// version is buildVersion, or else the version Go recorded at build time.
func version() string {
	if buildVersion != "" {
		return buildVersion
	}
	info, _ := debug.ReadBuildInfo()
	return info.Main.Version
}

// instanceID is a new random UUID (version 4) for each run, the form
// OpenTelemetry suggests for service.instance.id.
func instanceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
