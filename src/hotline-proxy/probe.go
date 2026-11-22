package proxy

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
	"hotline-proxy/internal/tracectx"
)

// A probe is a check of a vendor endpoint, its host and port, after calls
// to it failed to connect: it finds out which side of the connection the
// problem is on, and sends the answer as one record and one trace
// (docs/decisions/probe.md). Every integration calling the endpoint shares
// its probes, and they back off while the failures go on, so a vendor
// behind a firewall is not hammered (Peter, 2026-11-21).

// probeChain is the ledger chain of the probes' records. They belong to no
// integration: an endpoint is shared by all of them.
const probeChain = "hotline.probe"

// probeWorkers is how many probes run at once. A probe waits for a free
// worker, so many failing vendors never make many probes at once.
const probeWorkers = 2

// probes keeps the probe schedule of every endpoint whose calls fail to
// connect, and runs the probes on a fixed pool of workers, apart from the
// calls.
type probes struct {
	clock Clock
	// dial looks up and connects as calls do, with the same resolver and
	// dialer.
	dial  *dialer
	roots *x509.CertPool
	// config is the config in use now, for each endpoint's settings.
	config func() *config.Config
	book   *book

	mu        sync.Mutex
	schedules map[string]*probeSchedule
	// dueJobs are the probes due, in the order they became due, each
	// endpoint at most once, and jobAdded wakes a worker when one is added or
	// the probes stop.
	dueJobs  []probeJob
	jobAdded *sync.Cond
	stopped  bool
	// cancel ends the probes still running, when a stop runs out of time.
	cancel  context.CancelFunc
	workers sync.WaitGroup
}

// probeSchedule is one vendor endpoint's probe schedule. It exists from the
// first failed call until a call connects, or no call fails for a while.
type probeSchedule struct {
	key    string
	target probeTarget
	// running says a probe is waiting for a worker or running.
	running bool
	// failedMeanwhile says a call failed while a probe ran or before the next was
	// due, so the next probe starts once it is due. causeSpan is the last
	// failed call's span, which the probe links to.
	failedMeanwhile bool
	causeSpan       trace.SpanContext
	// nextWait is how long after the next probe starts the one after it may
	// start; zero before the first.
	nextWait    time.Duration
	dueAt       time.Time
	dueTimer    Timer
	lastFailure time.Time
	quietTimer  Timer
	// latestProbe is the latest probe that finished, which call records point
	// to; nil until one has.
	latestProbe *probeLink
}

// probeTarget is what a probe needs to know about the endpoint, from the
// call that failed.
type probeTarget struct {
	host string
	port string
	// tls says calls go over https, so the probe checks TLS too.
	tls bool
	// allowInternal says the api lets calls reach internal IPs, so the
	// probe may too.
	allowInternal bool
}

// probeJob is a probe waiting for a worker.
type probeJob struct {
	e         *probeSchedule
	causeSpan trace.SpanContext
}

// probeLink is what a call record carries of the latest probe of its
// endpoint (Peter, 2026-11-22).
type probeLink struct {
	recordHash string
	traceID    trace.TraceID
	side       string
	verdict    string
	ended      time.Time
}

// callOutcome is what a call's record tells the probes.
type callOutcome int

const (
	// failedToConnect is a call that could not reach its vendor: DNS, the
	// TCP connect or TLS failed.
	failedToConnect callOutcome = iota
	// connected is a call that reached its vendor, whatever it answered.
	connected
	// neverTried is a call that never tried, such as one to an internal
	// IP the proxy refused.
	neverTried
)

func newProbes(clock Clock, dial *dialer, roots *x509.CertPool, current func() *config.Config, b *book) *probes {
	ctx, cancel := context.WithCancel(context.Background())
	p := &probes{
		clock: clock, dial: dial, roots: roots, config: current, book: b,
		schedules: map[string]*probeSchedule{}, cancel: cancel,
	}
	p.jobAdded = sync.NewCond(&p.mu)
	p.workers.Add(probeWorkers)
	for range probeWorkers {
		go func() {
			defer p.workers.Done()
			for job, found := p.nextJob(); found; job, found = p.nextJob() {
				p.runProbe(ctx, job)
			}
		}()
	}
	return p
}

// nextJob waits for a due probe. Once the probes stop, the ones still due are
// handed out, then none.
func (p *probes) nextJob() (probeJob, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.dueJobs) == 0 && !p.stopped {
		p.jobAdded.Wait()
	}
	if len(p.dueJobs) == 0 {
		return probeJob{}, false
	}
	job := p.dueJobs[0]
	p.dueJobs = p.dueJobs[1:]
	return job, true
}

// noteCall tells the probes how a call to an endpoint went, from its record,
// and gives back the latest probe of that endpoint for the record to carry,
// or nil when there is none. A failed connection starts a probe, at once or
// once the next is due; a call that connected ends the endpoint's
// schedule.
func (p *probes) noteCall(t probeTarget, outcome callOutcome, cause trace.SpanContext) *probeLink {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := net.JoinHostPort(t.host, t.port)
	e := p.schedules[key]
	var latest *probeLink
	if e != nil {
		latest = e.latestProbe
	}
	switch outcome {
	case connected:
		if e != nil {
			p.endSchedule(e)
		}
		return latest
	case neverTried:
		return latest
	}
	settings := p.config().ProbeFor(t.host)
	if !settings.Enabled {
		if e != nil {
			p.endSchedule(e)
		}
		return nil
	}
	if e == nil {
		e = &probeSchedule{key: key, dueTimer: noTimer{}}
		e.quietTimer = p.clock.AfterFunc(settings.QuietAfter, func() { p.quietCheck(e) })
		p.schedules[key] = e
	}
	// The newest call's settings: a reload may have changed them.
	e.target = t
	e.lastFailure, e.causeSpan = p.clock.Now(), cause
	if e.running || e.lastFailure.Before(e.dueAt) {
		e.failedMeanwhile = true
		return latest
	}
	p.queueProbe(e)
	return latest
}

// queueProbe hands a probe of e to the workers. It is called with p.mu held.
func (p *probes) queueProbe(e *probeSchedule) {
	e.running, e.failedMeanwhile = true, false
	p.dueJobs = append(p.dueJobs, probeJob{e: e, causeSpan: e.causeSpan})
	p.jobAdded.Signal()
}

// endSchedule stops e's schedule: the next failure probes at once again. It is
// called with p.mu held.
func (p *probes) endSchedule(e *probeSchedule) {
	e.dueTimer.Stop()
	e.quietTimer.Stop()
	delete(p.schedules, e.key)
}

// noTimer is the wait of an endpoint with no probe due yet.
type noTimer struct{}

func (noTimer) Stop() bool { return false }

// stillScheduled says e is still the schedule of its endpoint, not one a
// connected call or a quiet spell ended. It is called with p.mu held.
func (p *probes) stillScheduled(e *probeSchedule) bool {
	return !p.stopped && p.schedules[e.key] == e
}

// dueNow runs when e's next probe is due: it starts when a call failed
// since the last one. A schedule that ended has nothing due.
func (p *probes) dueNow(e *probeSchedule) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stillScheduled(e) && e.failedMeanwhile && !e.running {
		p.queueProbe(e)
	}
}

// quietCheck ends e's schedule once no call to it has failed for
// quietAfter, or waits again until then.
func (p *probes) quietCheck(e *probeSchedule) {
	p.mu.Lock()
	defer p.mu.Unlock()
	left := e.lastFailure.Add(p.config().ProbeFor(e.target.host).QuietAfter).Sub(p.clock.Now())
	switch {
	case !p.stillScheduled(e):
		// A schedule that ended has nothing to wait for.
	case left > 0:
		e.quietTimer = p.clock.AfterFunc(left, func() { p.quietCheck(e) })
	default:
		p.endSchedule(e)
	}
}

// runProbe probes one endpoint, records what it found, and sets when the next
// probe may start: first after backoff.first, then after twice as long
// each time, up to backoff.longest (Peter, 2026-11-21).
func (p *probes) runProbe(ctx context.Context, job probeJob) {
	e := job.e
	p.mu.Lock()
	t := e.target
	p.mu.Unlock()
	cfg := p.config()
	settings := cfg.ProbeFor(t.host)
	start := p.clock.Now()
	found := p.probe(ctx, t, settings.Timeout)
	if ctx.Err() != nil {
		// The proxy stopped before the probe ended, so what it found
		// says nothing about the vendor.
		return
	}
	f := found.recordAndTrace(t, cfg, start, p.clock.Now(), job.causeSpan)
	f.validText()
	hash := p.book.record(recordContext(f.sc), f)
	p.mu.Lock()
	defer p.mu.Unlock()
	e.running = false
	if !p.stillScheduled(e) {
		return
	}
	e.latestProbe = &probeLink{recordHash: hash, traceID: f.sc.TraceID(), side: found.side, verdict: found.verdict, ended: f.spans[0].End}
	// The waits of the config in use now: a reload applies from the next
	// wait. One that turned the probe off ends the schedule at the next
	// failure.
	settings = p.config().ProbeFor(t.host)
	wait := cmp.Or(e.nextWait, settings.First)
	wait = min(wait, settings.Longest)
	e.nextWait = min(2*wait, settings.Longest)
	e.dueAt = start.Add(wait)
	e.dueTimer = p.clock.AfterFunc(max(e.dueAt.Sub(p.clock.Now()), 0), func() { p.dueNow(e) })
}

// stop ends every schedule and lets the probes already due finish, within
// the time ctx allows; those still running then are dropped.
func (p *probes) stop(ctx context.Context) {
	p.mu.Lock()
	for _, e := range p.schedules {
		p.endSchedule(e)
	}
	p.stopped = true
	p.jobAdded.Broadcast()
	p.mu.Unlock()
	done := make(chan struct{})
	go func() {
		p.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		p.cancel()
		<-done
	}
	// The workers are gone; this frees the context they shared.
	p.cancel()
}

// The answers to a question.
const (
	answerYes        = "yes"
	answerNo         = "no"
	answerNotChecked = "not_checked"
)

// The sides of the connection a problem can be on (Peter, 2026-11-21).
const (
	sideOurs    = "our_side"
	sideBetween = "between"
	sideVendor  = "vendor_side"
	sideNone    = "none"
)

// question is one question a probe answers, as it answered it.
type question struct {
	id string
	// text is the question in plain words.
	text   string
	answer string
	// pointsTo is the side a "no" points to.
	pointsTo string
	evidence []string
	// start and end time the check that answered it; zero when it was not
	// asked.
	start time.Time
	end   time.Time
}

// probeFound is what one probe found: its three questions, the side of the
// first "no", and the verdict.
type probeFound struct {
	questions [3]question
	side      string
	verdict   string
	// sentence is the verdict in plain words.
	sentence string
}

// probe asks the endpoint its questions, one after the other, each step
// waiting at most timeout by the proxy's clock: is the vendor's name right,
// does its port accept us, and does TLS work. It sends no HTTP request.
func (p *probes) probe(ctx context.Context, t probeTarget, timeout time.Duration) probeFound {
	endpoint := net.JoinHostPort(t.host, t.port)
	f := probeFound{questions: [3]question{
		{id: "name_resolves", text: "Is the vendor's name right?", pointsTo: sideOurs, answer: answerNotChecked},
		{id: "port_accepts_us", text: "Does the vendor's port accept us?", pointsTo: sideVendor, answer: answerNotChecked},
		{id: "tls_works", text: "Does the encryption (TLS) work?", pointsTo: sideVendor, answer: answerNotChecked},
	}}
	name, port, encryption := &f.questions[0], &f.questions[1], &f.questions[2]
	port.evidence = []string{"not checked: the vendor's name did not resolve"}
	encryption.evidence = port.evidence

	ips, verdict := p.resolve(ctx, t, timeout, name)
	if verdict != "" {
		return f.withVerdict(verdict, endpoint)
	}
	encryption.evidence = []string{"not checked: the vendor's port did not accept us"}
	conn, verdict := p.connect(ctx, net.JoinHostPort(ips[0].String(), t.port), timeout, port)
	if verdict != "" {
		return f.withVerdict(verdict, endpoint)
	}
	defer func() { _ = conn.Close() }()
	if !t.tls {
		encryption.evidence = []string{"not checked: calls to " + endpoint + " use plain HTTP"}
		return f.withVerdict("reachable_now", endpoint)
	}
	return f.withVerdict(p.handshake(ctx, conn, t.host, timeout, encryption), endpoint)
}

// resolve answers name_resolves: it looks up the host as a call does, and
// keeps only the addresses a call may use. It gives the verdict when the
// answer is no.
func (p *probes) resolve(ctx context.Context, t probeTarget, timeout time.Duration, q *question) ([]netip.Addr, string) {
	if ip, err := netip.ParseAddr(t.host); err == nil {
		q.answer, q.evidence = answerYes, []string{t.host + " is an IP address, so there is no name to look up"}
		return []netip.Addr{ip}, ""
	}
	ctx, _, done := p.stepContext(ctx, timeout)
	q.start = p.clock.Now()
	ips, err := p.dial.lookup(ctx, t.host)
	q.end = p.clock.Now()
	done()
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		q.answer, q.evidence = answerNo, []string{"the name " + t.host + " does not exist: " + err.Error()}
		return nil, "dns_name_not_found"
	case err != nil:
		q.answer, q.evidence = answerNo, []string{"the lookup of " + t.host + " failed: " + err.Error()}
		return nil, "dns_failed"
	}
	if !t.allowInternal {
		ips = slices.DeleteFunc(ips, internal)
	}
	if len(ips) == 0 {
		q.answer, q.evidence = answerNo, []string{t.host + " resolves only to internal IPs, which its calls may not reach"}
		return nil, "dns_failed"
	}
	shown := make([]string, len(ips))
	for i, ip := range ips {
		shown[i] = ip.String()
	}
	q.answer, q.evidence = answerYes, []string{t.host + " is " + strings.Join(shown, ", ")}
	return ips, ""
}

// connect answers port_accepts_us with one TCP connect to the vendor's
// first address. A refusal points to the vendor; silence, or a network
// that has no way there, points between, as only the path probes of a
// later slice can tell where it stops.
func (p *probes) connect(ctx context.Context, address string, timeout time.Duration, q *question) (net.Conn, string) {
	ctx, timedOut, done := p.stepContext(ctx, timeout)
	defer done()
	q.start = p.clock.Now()
	conn, err := p.dial.dialer.DialContext(ctx, "tcp", address)
	q.end = p.clock.Now()
	took := q.end.Sub(q.start)
	switch {
	case err == nil:
		q.answer, q.evidence = answerYes, []string{address + " accepted the connection in " + millis(took)}
		return conn, ""
	case timedOut():
		q.answer, q.pointsTo = answerNo, sideBetween
		q.evidence = []string{fmt.Sprintf("%s sent no answer within %s: something drops our packets", address, timeout)}
		return nil, "no_answer"
	case errors.Is(err, syscall.ECONNREFUSED):
		q.answer = answerNo
		q.evidence = []string{address + " answered with a reset in " + millis(took) + ": it refused the connection"}
		return nil, "refused_by_vendor"
	}
	q.answer, q.pointsTo = answerNo, sideBetween
	q.evidence = []string{address + " could not be reached: " + err.Error()}
	return nil, "unreachable"
}

// handshake answers tls_works with one TLS handshake, checked as a call's
// is, with the call's host name.
func (p *probes) handshake(ctx context.Context, conn net.Conn, host string, timeout time.Duration, q *question) string {
	ctx, timedOut, done := p.stepContext(ctx, timeout)
	defer done()
	client := tls.Client(conn, &tls.Config{ServerName: host, RootCAs: p.roots, MinVersion: tls.VersionTLS12})
	q.start = p.clock.Now()
	err := client.HandshakeContext(ctx)
	q.end = p.clock.Now()
	switch {
	case err == nil:
		state := client.ConnectionState()
		q.answer = answerYes
		q.evidence = []string{fmt.Sprintf("%s with %s, its certificate valid until %s", tls.VersionName(state.Version), host,
			state.PeerCertificates[0].NotAfter.UTC().Format(time.RFC3339))}
		return "reachable_now"
	case timedOut():
		q.answer, q.evidence = answerNo, []string{fmt.Sprintf("TLS with %s got no answer within %s", host, timeout)}
		return "tls_failed"
	case isCertificateError(err):
		q.answer, q.evidence = answerNo, []string{"the certificate of " + host + " failed the check: " + err.Error()}
		return "tls_certificate_failed"
	case errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET):
		q.answer, q.evidence = answerNo, []string{"the vendor closed the connection as TLS started: " + err.Error()}
		return "tls_cut_by_vendor"
	}
	q.answer, q.evidence = answerNo, []string{"TLS with " + host + " failed: " + err.Error()}
	return "tls_failed"
}

// stepContext is the context of one step of a probe, which ends after timeout by
// the proxy's clock. timedOut says whether it did; done ends the wait.
func (p *probes) stepContext(ctx context.Context, timeout time.Duration) (context.Context, func() bool, func()) {
	ctx, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	over := false
	timer := p.clock.AfterFunc(timeout, func() {
		mu.Lock()
		over = true
		mu.Unlock()
		cancel()
	})
	timedOut := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return over
	}
	return ctx, timedOut, func() {
		timer.Stop()
		cancel()
	}
}

// millis is a short time in milliseconds, as people read it.
func millis(d time.Duration) string {
	return strconv.FormatFloat(float64(d.Microseconds())/1000, 'f', 1, 64) + " ms"
}

// withVerdict sets the side from the first "no", and the verdict and its
// sentence.
func (f probeFound) withVerdict(verdict, endpoint string) probeFound {
	f.side, f.verdict = sideNone, verdict
	for _, q := range f.questions {
		if q.answer == answerNo {
			f.side = q.pointsTo
			break
		}
	}
	f.sentence = verdictSentence(verdict, endpoint)
	return f
}

// verdictSentence is a verdict in one plain sentence.
func verdictSentence(verdict, endpoint string) string {
	host, _, _ := net.SplitHostPort(endpoint)
	switch verdict {
	case "dns_name_not_found":
		return "The name " + host + " does not exist in DNS: check the vendor's host name on our side."
	case "dns_failed":
		return "The DNS lookup of " + host + " failed: check our DNS resolver."
	case "refused_by_vendor":
		return endpoint + " refused the connection: the vendor's server or firewall turned us away."
	case "no_answer":
		return endpoint + " did not answer: a firewall between us, or the vendor's, may drop our packets."
	case "unreachable":
		return endpoint + " could not be reached: the network has no way there."
	case "tls_cut_by_vendor":
		return endpoint + " took the connection but closed it as TLS started: the vendor's side ended it."
	case "tls_certificate_failed":
		return endpoint + " showed a certificate that failed the check: the vendor's certificate is wrong for us."
	case "tls_failed":
		return endpoint + " took the connection but TLS failed: the vendor's side does not speak TLS as we do."
	}
	return endpoint + " can be reached now: the failure has passed."
}

// recordAndTrace is the probe's record and trace: a root span "probe", linked
// to the failed call that made the probe due, and under it one span for
// each question asked.
func (f probeFound) recordAndTrace(t probeTarget, cfg *config.Config, start, end time.Time, cause trace.SpanContext) finished {
	port, _ := strconv.Atoi(t.port)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: tracectx.W3C.NewTraceID(start), SpanID: tracectx.NewSpanID(), TraceFlags: trace.FlagsSampled,
	})
	about := []attribute.KeyValue{
		semconv.ServerAddress(t.host),
		semconv.ServerPort(port),
		attribute.String("hotline.probe.side", f.side),
		attribute.String("hotline.probe.verdict", f.verdict),
	}
	// Room for the ledger's four attributes too.
	attrs := make([]attribute.KeyValue, 0, len(about)+2*len(f.questions)+4)
	attrs = append(attrs, about...)
	root := export.Span{
		ID: sc.SpanID(), Name: "probe", Kind: trace.SpanKindInternal, Start: start, End: end,
		Status: export.StatusError, Attrs: about,
	}
	if f.side == sideNone {
		root.Status = export.StatusOK
	}
	if cause.IsValid() {
		root.Links = []trace.SpanContext{cause}
	}
	spans := []export.Span{root}
	for _, q := range f.questions {
		prefix := "hotline.probe." + q.id
		attrs = append(attrs, attribute.String(prefix+".answer", q.answer), attribute.StringSlice(prefix+".evidence", q.evidence))
		if q.start.IsZero() {
			continue
		}
		pointsTo := sideNone
		if q.answer == answerNo {
			pointsTo = q.pointsTo
		}
		spans = append(spans, export.Span{
			ID: tracectx.NewSpanID(), Parent: root.ID, Name: q.id, Kind: trace.SpanKindInternal,
			Start: q.start, End: q.end, Status: questionStatus(q.answer),
			Attrs: []attribute.KeyValue{
				attribute.String("hotline.probe.question", q.text),
				attribute.String("hotline.probe.answer", q.answer),
				attribute.String("hotline.probe.points_to", pointsTo),
				attribute.StringSlice("hotline.probe.evidence", q.evidence),
			},
		})
	}
	return finished{
		event: export.ProbeEvent, cfg: cfg, chain: probeChain, sc: sc, at: start,
		attrs: attrs, body: f.sentence, spans: spans,
	}
}

// questionStatus is a question's span status: ok for yes, error for no.
func questionStatus(answer string) export.Status {
	if answer == answerYes {
		return export.StatusOK
	}
	return export.StatusError
}

// probeAttrs are what a call record carries of the latest probe of its
// endpoint: its record's hash and its trace id, and for a failed call also
// what it found and when (Peter, 2026-11-22).
func probeAttrs(attrs []attribute.KeyValue, link *probeLink, failed bool) []attribute.KeyValue {
	if link == nil {
		return attrs
	}
	attrs = append(attrs,
		attribute.String("hotline.probe.record_hash", link.recordHash),
		attribute.String("hotline.probe.trace_id", link.traceID.String()))
	if failed {
		attrs = append(attrs,
			attribute.String("hotline.probe.side", link.side),
			attribute.String("hotline.probe.verdict", link.verdict),
			attribute.String("hotline.probe.time", link.ended.UTC().Format(time.RFC3339Nano)))
	}
	return attrs
}

// connectFailure says the error type is of a call that could not connect
// to its vendor, which makes a probe due. A call the proxy refused by
// its own rule, or whose connection worked, makes none.
func connectFailure(errorType string) bool {
	switch errorType {
	case "dns_error", "dns_timeout", "connection_refused", "connection_timeout",
		"destination_unavailable", "tls_certificate_error", "tls_protocol_error":
		return true
	}
	return false
}
