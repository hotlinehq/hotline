package proxy

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"

	"hotline-proxy/internal/export"
	"hotline-proxy/internal/ledger"
)

// The exit codes of verify, in both formats.
const (
	verifyOK         = 0
	verifyProblem    = 1
	verifyUnreadable = 2
)

// errUnreadable is input verify cannot read.
var errUnreadable = errors.New("input cannot be read")

// VerifyOptions say what hotline-proxy verify checks and how it prints.
type VerifyOptions struct {
	// Folder is a file exporter's folder.
	Folder string
	// Integration, when set, checks that integration's records alone.
	Integration string
	// Body, Span and Side, when set, check a body someone kept against the
	// hash in the record with that span id: request or response.
	Body, Span, Side string
	// JSON prints one JSON object in place of plain lines.
	JSON bool
}

// Verify checks the records a file exporter wrote, with nothing but the
// lines (docs/proxy-ledger.md): each record's hash and its trace's, each
// batch's root and signature, and each run's chain. It prints what it
// found and gives the exit code: 0 when everything checks out, 1 for a
// problem, 2 when it could not read its input.
func Verify(out io.Writer, o VerifyOptions) int {
	v := &verifier{o: o}
	err := v.read()
	if err == nil {
		v.check()
	}
	code := verifyOK
	switch {
	case err != nil:
		code = verifyUnreadable
		v.problems = append(v.problems, finding{Code: "input_unreadable", Message: err.Error()})
	case len(v.problems) > 0:
		code = verifyProblem
	}
	if o.JSON {
		v.printJSON(out, code)
	} else {
		v.printText(out)
	}
	return code
}

type verifier struct {
	o       VerifyOptions
	records []*lineRecord
	batches []*lineBatch
	// drops are the drop records, by the id of their batch, and dropsFrom
	// the exporter the first one taken names.
	drops     map[string][]*lineBatch
	dropsFrom string
	runs      []*verifiedRun
	// problems and notes are what the checks found; matched and cut count
	// the kept bodies checked and those cut short.
	problems []finding
	notes    []finding
	matched  int
	cut      int
	body     *bodyResult
}

// finding is one problem or note, as the JSON output holds it.
type finding struct {
	Code        string  `json:"code"`
	Integration string  `json:"integration,omitempty"`
	Instance    string  `json:"instance,omitempty"`
	Batch       *int64  `json:"batch,omitempty"`
	BatchID     string  `json:"batch_id,omitempty"`
	Index       *int64  `json:"index,omitempty"`
	SpanID      string  `json:"span_id,omitempty"`
	File        string  `json:"file,omitempty"`
	Line        int     `json:"line,omitempty"`
	Message     string  `json:"message"`
	Indexes     []int64 `json:"indexes,omitempty"`
}

type bodyResult struct {
	SpanID  string `json:"span_id"`
	Side    string `json:"side"`
	Matches bool   `json:"matches"`
}

// place is where a line was read.
type place struct {
	file string
	line int
}

// lineRecord is a call's record as one line holds it.
type lineRecord struct {
	place
	read            export.Line
	traceID, spanID string
	attrs           map[string]any
	batchID         string
	index           int64
	hash            string
}

func (r *lineRecord) text(name string) string {
	s, _ := r.attrs[name].(string)
	return s
}

// lineBatch is a batch or drop record as one line holds it.
type lineBatch struct {
	place
	read  export.Line
	at    time.Time
	attrs map[string]any
	id    string
	// records are the records found that name the batch, by their index.
	records map[int64]*lineRecord
}

func (b *lineBatch) text(name string) string {
	s, _ := b.attrs[name].(string)
	return s
}

func (b *lineBatch) number(name string) (int64, bool) {
	n, ok := b.attrs[name].(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil
}

// verifiedRun is one run of one integration's chain: its batches in
// sequence order.
type verifiedRun struct {
	Integration   string   `json:"integration"`
	Instance      string   `json:"instance"`
	Run           *string  `json:"run"`
	FirstSequence int64    `json:"first_sequence"`
	LastSequence  int64    `json:"last_sequence"`
	Batches       int      `json:"batches"`
	Records       int      `json:"records"`
	CleanStop     bool     `json:"clean_stop"`
	StoppedAt     *string  `json:"stopped_at"`
	Notes         []string `json:"notes"`
	batches       []*lineBatch
}

// read reads every file of the folder, the rotated ones first, oldest
// first, then the current one.
func (v *verifier) read() error {
	entries, err := os.ReadDir(v.o.Folder)
	if err != nil {
		return fmt.Errorf("%w: the folder %s does not exist or cannot be read", errUnreadable, v.o.Folder)
	}
	var rotated []string
	current := false
	for _, e := range entries {
		name := e.Name()
		switch {
		case name == "calls.jsonl":
			current = true
		case strings.HasPrefix(name, "calls-") && (strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".jsonl.gz")):
			rotated = append(rotated, name)
		}
	}
	slices.Sort(rotated)
	if current {
		rotated = append(rotated, "calls.jsonl")
	}
	for _, name := range rotated {
		if err := v.readFile(name); err != nil {
			return err
		}
	}
	return nil
}

func (v *verifier) readFile(name string) error {
	path := filepath.Join(v.o.Folder, name)
	data, err := os.ReadFile(path) //nolint:gosec // the folder the person named
	if err != nil {
		return fmt.Errorf("%w: %s cannot be read", errUnreadable, path)
	}
	if strings.HasSuffix(name, ".gz") {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("%w: %s is not gzip", errUnreadable, path)
		}
		if data, err = io.ReadAll(zr); err != nil {
			return fmt.Errorf("%w: %s is cut off or damaged", errUnreadable, path)
		}
	}
	for n, line := range bytes.Split(data, []byte("\n")) {
		// Blank lines are skipped.
		if len(bytes.TrimSpace(line)) > 0 {
			if err := v.readLine(place{file: path, line: n + 1}, line); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *verifier) readLine(at place, data []byte) error {
	line, err := export.ReadLine(data)
	switch {
	case errors.Is(err, export.ErrNoTime):
		return fmt.Errorf("%w: %s line %d has no time", errUnreadable, at.file, at.line)
	case err != nil:
		return fmt.Errorf("%w: %s line %d is not a line the file exporter writes", errUnreadable, at.file, at.line)
	}
	switch line.Event {
	case export.BatchEvent:
		b := &lineBatch{place: at, read: line, at: line.Time, attrs: line.Attributes, records: map[int64]*lineRecord{}}
		switch format := b.text("hotline.batch.format"); {
		case format == oldBatchFormat:
			return fmt.Errorf("%w: %s line %d: the folder was written by an older edge build (%s), which this verify cannot check", errUnreadable, at.file, at.line, oldBatchFormat)
		case format != ledger.BatchFormat:
			return fmt.Errorf("%w: %s line %d: %s is not a known format", errUnreadable, at.file, at.line, safe(format))
		}
		b.id = b.text("hotline.batch.id")
		if v.o.Integration == "" || b.text("hotline.batch.integration") == v.o.Integration {
			v.batches = append(v.batches, b)
		}
	case export.DroppedEvent:
		d := &lineBatch{place: at, read: line, at: line.Time, attrs: line.Attributes}
		d.id = d.text("hotline.batch.id")
		if v.drops == nil {
			v.drops = map[string][]*lineBatch{}
		}
		v.drops[d.id] = append(v.drops[d.id], d)
	case export.CallEvent, export.SessionEvent, export.MessageEvent, export.StreamEvent, export.StreamItemEvent, export.ProbeEvent:
		r := &lineRecord{place: at, read: line, traceID: line.TraceID, spanID: line.SpanID, attrs: line.Attributes}
		r.batchID = r.text("hotline.batch.id")
		r.hash = r.text("hotline.record.hash")
		if n, ok := r.attrs["hotline.batch.index"].(json.Number); ok {
			r.index, _ = n.Int64()
		}
		if v.o.Integration == "" || r.text("hotline.integration.id") == v.o.Integration {
			v.records = append(v.records, r)
		}
	}
	return nil
}

// check runs every check, in the order a reader takes them in: records,
// batches, then chains.
func (v *verifier) check() {
	byID := map[string]*lineBatch{}
	for _, b := range v.batches {
		byID[b.id] = b
	}
	unbatched := map[string][]*lineRecord{}
	for _, r := range v.records {
		b := byID[r.batchID]
		v.checkRecord(r, b)
		if b == nil {
			unbatched[r.batchID] = append(unbatched[r.batchID], r)
			continue
		}
		if _, taken := b.records[r.index]; taken {
			v.problem(finding{Code: "record_extra", SpanID: r.spanID, File: r.file, Line: r.line,
				Message: "batch " + b.sequenceText() + " holds a record it does not count"}, b)
			continue
		}
		b.records[r.index] = r
	}
	for _, b := range v.batches {
		v.checkBatch(b)
	}
	v.checkChains()
	for _, id := range slices.Sorted(maps.Keys(unbatched)) {
		records := unbatched[id]
		v.notes = append(v.notes, finding{Code: "records_unbatched", BatchID: id, File: records[0].file, Line: records[0].line,
			Message: fmt.Sprintf("the records naming batch %s have no batch record (%d records)", safe(id), len(records))})
	}
	if v.o.Body != "" {
		v.checkBody()
	}
}

func (v *verifier) checkRecord(r *lineRecord, b *lineBatch) {
	var attrs, content []attribute.KeyValue
	for name, value := range r.attrs {
		switch {
		case ledger.RecordCovers(name):
			attrs = append(attrs, attribute.KeyValue{Key: attribute.Key(name), Value: valueOf(value)})
		case strings.HasPrefix(name, "hotline.content.") && !strings.HasSuffix(name, ".body"):
			content = append(content, attribute.KeyValue{Key: attribute.Key(name), Value: valueOf(value)})
		}
	}
	at := finding{SpanID: r.spanID, File: r.file, Line: r.line}
	if b != nil {
		// A problem in a record names the batch it belongs to.
		at = v.about(at, b)
		index := r.index
		at.Index = &index
	}
	if ledger.Text(ledger.RecordHash(recordOf(r.read, attrs))) != r.hash {
		at.Code, at.Message = "record_hash_mismatch", "the record with span id "+safe(r.spanID)+" does not match its hotline.record.hash"
		v.problems = append(v.problems, at)
		return
	}
	if want := r.text("hotline.trace.hash"); want != "" && !traceMatches(r.traceID, r.read.Spans, want) {
		at.Code, at.Message = "trace_hash_mismatch", "the trace of the record with span id "+safe(r.spanID)+" does not match its hotline.trace.hash"
		v.problems = append(v.problems, at)
	}
	if want := r.text("hotline.content.hash"); want != "" && ledger.Text(ledger.ContentHash(content)) != want {
		at.Code, at.Message = "content_hash_mismatch", "the content kept with the record with span id "+safe(r.spanID)+" does not match its hotline.content.hash"
		v.problems = append(v.problems, at)
	}
	for _, body := range savedBodies() {
		v.checkKeptBody(r, body, at)
	}
}

// savedBody is a body content: full can save in a line, by its side, and
// the attribute that holds its hash in the record.
type savedBody struct{ side, hashAttribute string }

// savedBodies are the bodies a line can save: a call's request and
// response, and a stream record's stream.
func savedBodies() []savedBody {
	return []savedBody{
		{"request", "hotline.request.body.hash"},
		{"response", "hotline.response.body.hash"},
		{"stream", "hotline.stream.hash"},
	}
}

// checkKeptBody checks a body content: full kept in the line against the
// record's hash of that body, unless it was cut short.
func (v *verifier) checkKeptBody(r *lineRecord, body savedBody, at finding) {
	side := body.side
	saved, found := r.attrs["hotline.content."+side+".body"].(string)
	if !found {
		return
	}
	if cut, _ := r.attrs["hotline.content."+side+".body.truncated"].(bool); cut {
		v.cut++
		note := at
		note.Code, note.Message = "body_not_checked", "the "+side+" body kept with the record with span id "+safe(r.spanID)+" was cut short and could not be checked"
		v.notes = append(v.notes, note)
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(saved)
	if err == nil && bodyHashOf(side, r.traceID, r.spanID, decoded) == r.text(body.hashAttribute) {
		v.matched++
		return
	}
	at.Code, at.Message = "body_hash_mismatch", "the "+side+" body kept with the record with span id "+safe(r.spanID)+" does not match its hash"
	v.problems = append(v.problems, at)
}

func bodyHashOf(side, traceID, spanID string, body []byte) string {
	var t [16]byte
	var s [8]byte
	_, _ = hex.Decode(t[:], []byte(traceID))
	_, _ = hex.Decode(s[:], []byte(spanID))
	h := ledger.NewBodyHash(side, t, s)
	_, _ = h.Write(body)
	return h.Sum()
}

func (b *lineBatch) sequenceText() string {
	if n, ok := b.number("hotline.batch.sequence"); ok {
		return strconv.FormatInt(n, 10)
	}
	return safe(b.id)
}

// problem adds a problem found in a batch, naming the batch.
func (v *verifier) problem(f finding, b *lineBatch) {
	v.problems = append(v.problems, v.about(f, b))
}

func (v *verifier) about(f finding, b *lineBatch) finding {
	f.Integration, f.Instance, f.BatchID = b.text("hotline.batch.integration"), b.text("hotline.batch.instance"), b.id
	if n, ok := b.number("hotline.batch.sequence"); ok {
		f.Batch = &n
	}
	if f.File == "" {
		f.File, f.Line = b.file, b.line
	}
	return f
}

func (v *verifier) checkBatch(b *lineBatch) {
	name := "batch " + b.sequenceText()
	if !ledger.CheckSignature(ledger.BatchFormat, recordOf(b.read, attributes(b.attrs))) {
		v.problem(finding{Code: "batch_signature_invalid", Message: "the signature of " + name + " does not check out with its key"}, b)
	}
	// The drop record of this copy, if its exporter dropped records of the
	// batch: signed with the batch's own key, or it is not the proxy's.
	// A copy is one exporter's, so it holds one drop record of a batch at
	// most, and all its drop records name the same exporter.
	drop := &lineBatch{attrs: map[string]any{}}
	for _, d := range v.drops[b.id] {
		exporter := d.text("hotline.exporter")
		switch {
		case d.text("hotline.batch.key") != b.text("hotline.batch.key") || !ledger.CheckSignature(ledger.DroppedFormat, recordOf(d.read, attributes(d.attrs))):
			v.problem(finding{Code: "drop_signature_invalid", File: d.file, Line: d.line,
				Message: "the drop record of " + name + " does not check out with the batch's key"}, b)
			continue
		case drop.id != "":
			v.problem(finding{Code: "drop_record_foreign", File: d.file, Line: d.line,
				Message: name + " has more than one drop record in this copy"}, b)
			continue
		case v.dropsFrom != "" && exporter != v.dropsFrom:
			v.problem(finding{Code: "drop_record_foreign", File: d.file, Line: d.line,
				Message: fmt.Sprintf("the drop record of %s is from the exporter %s, and this copy's other drop records from %s", name, safe(exporter), safe(v.dropsFrom))}, b)
			continue
		}
		v.dropsFrom, drop = exporter, d
	}
	count, _ := b.number("hotline.batch.records")
	if dropped, _ := drop.number("hotline.batch.dropped_count"); dropped > 0 {
		v.problem(finding{Code: "batch_unprovable", Message: fmt.Sprintf("%s cannot be proven from this copy: %s records were dropped by this exporter", name, thousands(dropped))}, b)
		return
	}
	leaves := make([][sha256.Size]byte, count)
	have := make([]bool, count)
	for index, r := range b.records {
		if index < 0 || index >= count {
			v.problem(finding{Code: "record_extra", SpanID: r.spanID, File: r.file, Line: r.line, Message: name + " holds a record it does not count"}, b)
			continue
		}
		leaves[index], have[index] = parseHash(r.hash), true
	}
	listed, _ := drop.attrs["hotline.batch.dropped"].([]any)
	var droppedAt []int64
	for _, item := range listed {
		entry, _ := item.(map[string]any)
		n, _ := entry["index"].(json.Number)
		index, err := n.Int64()
		hash, _ := entry["hash"].(string)
		switch {
		case err != nil || index < 0 || index >= count:
			// An entry that names no place in the batch is left out.
		case have[index]:
			// The exporter dropped the record, so its copy cannot hold it:
			// the drop record is another copy's.
			v.problem(finding{Code: "drop_record_foreign", File: drop.file, Line: drop.line, Index: &index,
				Message: fmt.Sprintf("the drop record of %s lists index %d, which this copy holds", name, index)}, b)
		default:
			leaves[index], have[index] = parseHash(hash), true
			droppedAt = append(droppedAt, index)
		}
	}
	var missing []int64
	for i, ok := range have {
		if !ok {
			missing = append(missing, int64(i))
		}
	}
	if len(missing) > 0 {
		v.problem(finding{Code: "record_missing", Index: &missing[0], Indexes: missing,
			Message: fmt.Sprintf("%s has %d of %d records and index %s is missing", name, count-int64(len(missing)), count, joinInts(missing))}, b)
		return
	}
	if len(droppedAt) > 0 {
		slices.Sort(droppedAt)
		v.notes = append(v.notes, v.about(finding{Code: "records_dropped", Indexes: droppedAt,
			Message: fmt.Sprintf("%s rebuilds from %d records and %d dropped hash, at index %s, dropped by the exporter %s", name, count-int64(len(droppedAt)), len(droppedAt), joinInts(droppedAt), safe(drop.text("hotline.exporter")))}, b))
	}
	if ledger.Text(ledger.Root(leaves)) != b.text("hotline.batch.root") {
		v.problem(finding{Code: "batch_root_mismatch", Message: name + " of that run does not rebuild its root from its records"}, b)
	}
}

func parseHash(text string) [sha256.Size]byte {
	sum, _ := ledger.Parse(text)
	return sum
}

// checkChains groups the batches into runs, one per integration, instance
// and run id, and checks each run's chain: every sequence from 0, each
// batch naming the root of the one before, and a last batch at the end.
func (v *verifier) checkChains() {
	type key struct{ integration, instance, run string }
	runs := map[key]*verifiedRun{}
	var order []key
	for _, b := range v.batches {
		k := key{b.text("hotline.batch.integration"), b.text("hotline.batch.instance"), b.text("hotline.batch.run")}
		run := runs[k]
		if run == nil {
			run = &verifiedRun{Integration: k.integration, Instance: k.instance, Notes: []string{}}
			if k.run != "" {
				id := k.run
				run.Run = &id
			}
			runs[k] = run
			order = append(order, k)
		}
		run.batches = append(run.batches, b)
	}
	for _, k := range order {
		run := runs[k]
		slices.SortStableFunc(run.batches, func(a, b *lineBatch) int {
			x, _ := a.number("hotline.batch.sequence")
			y, _ := b.number("hotline.batch.sequence")
			return cmp.Compare(x, y)
		})
		run.Batches = len(run.batches)
		expect := int64(0)
		var before *lineBatch
		for _, b := range run.batches {
			seq, _ := b.number("hotline.batch.sequence")
			run.Records += len(b.records)
			for ; expect < seq; expect++ {
				missing := expect
				v.problems = append(v.problems, v.about(finding{Code: "batch_missing", Batch: &missing,
					Message: fmt.Sprintf("the run's chain is broken: batch %d is missing", missing)}, b))
				before = nil
			}
			switch {
			case seq < expect:
				v.problem(finding{Code: "chain_broken", Message: fmt.Sprintf("the chain is broken: batch %d is there twice", seq)}, b)
			case before != nil && b.text("hotline.batch.previous") != before.text("hotline.batch.root"):
				v.problem(finding{Code: "chain_broken", Message: fmt.Sprintf("the chain is broken between batch %d and batch %d", seq-1, seq)}, b)
			case seq == 0 && b.text("hotline.batch.previous") != "":
				v.problem(finding{Code: "chain_broken", Message: "the chain is broken: batch 0 names a batch before it"}, b)
			}
			before = b
			expect = seq + 1
		}
		run.FirstSequence, _ = run.batches[0].number("hotline.batch.sequence")
		last := run.batches[len(run.batches)-1]
		run.LastSequence, _ = last.number("hotline.batch.sequence")
		if clean, _ := last.attrs["hotline.batch.last"].(bool); clean {
			run.CleanStop = true
			stopped := last.at.UTC().Format(time.RFC3339)
			run.StoppedAt = &stopped
		} else {
			run.Notes = append(run.Notes, "no_last_batch")
			v.notes = append(v.notes, v.about(finding{Code: "no_last_batch", Message: "the run has no last batch: it crashed, or its end is missing"}, last))
		}
		v.runs = append(v.runs, run)
	}
}

// checkBody checks a body someone kept against the record with its span
// id.
func (v *verifier) checkBody() {
	data, err := os.ReadFile(v.o.Body)
	if err != nil {
		v.problems = append(v.problems, finding{Code: "body_unreadable", Message: "the body " + safe(v.o.Body) + " cannot be read"})
		return
	}
	for _, r := range v.records {
		if r.spanID == v.o.Span {
			matches := bodyHashOf(v.o.Side, r.traceID, r.spanID, data) == r.text("hotline."+v.o.Side+".body.hash")
			v.body = &bodyResult{SpanID: r.spanID, Side: v.o.Side, Matches: matches}
			if !matches {
				v.problems = append(v.problems, finding{Code: "body_mismatch", SpanID: r.spanID, File: r.file, Line: r.line,
					Message: "the " + v.o.Side + " body does not match the record's hotline." + v.o.Side + ".body.hash"})
			}
			return
		}
	}
	v.problems = append(v.problems, finding{Code: "body_record_missing", SpanID: safe(v.o.Span), Message: "no record has the span id " + safe(v.o.Span)})
}

func (v *verifier) printText(out io.Writer) {
	w := &lines{out: out}
	for _, run := range v.runs {
		ending := "the run ended with a clean stop"
		if !run.CleanStop {
			ending = "the run has no last batch: it crashed, or its end is missing"
		}
		instance := "instance " + safe(run.Instance)
		if run.Run != nil {
			instance += ", run " + safe(*run.Run)
		}
		w.printf("%s's %d records and their traces in %d batches check out (%s, sequence %d to %d); %s",
			safe(run.Integration), run.Records, run.Batches, instance, run.FirstSequence, run.LastSequence, ending)
	}
	if v.matched > 0 {
		w.printf("%d kept bodies match their hash", v.matched)
	}
	if v.cut > 0 {
		w.printf("%d kept bodies were cut short and could not be checked", v.cut)
	}
	if v.body != nil && v.body.Matches {
		w.printf("the %s body matches the record's hotline.%s.body.hash", v.body.Side, v.body.Side)
	}
	for _, n := range v.notes {
		w.printf("note: %s", n.Message)
	}
	for _, p := range v.problems {
		where := ""
		if p.File != "" {
			where = fmt.Sprintf("%s line %d: ", safe(p.File), p.Line)
		}
		w.printf("problem: %s%s", where, p.Message)
	}
	verdict := "everything checks out"
	switch len(v.problems) {
	case 0:
	case 1:
		verdict = "1 problem found"
	default:
		verdict = fmt.Sprintf("%d problems found", len(v.problems))
	}
	w.printf("%d records, %d batches, %d runs: %s", len(v.records), len(v.batches), len(v.runs), verdict)
}

// lines prints plain lines, each one safe for a terminal.
type lines struct{ out io.Writer }

func (l *lines) printf(format string, args ...any) {
	_, _ = fmt.Fprintln(l.out, fmt.Sprintf(format, args...))
}

func (v *verifier) printJSON(out io.Writer, code int) {
	runs := v.runs
	if runs == nil {
		runs = []*verifiedRun{}
	}
	problems, notes := v.problems, v.notes
	if problems == nil {
		problems = []finding{}
	}
	if notes == nil {
		notes = []finding{}
	}
	report := struct {
		Format  string `json:"format"`
		OK      bool   `json:"ok"`
		Summary struct {
			Records      int `json:"records"`
			Batches      int `json:"batches"`
			Runs         int `json:"runs"`
			Problems     int `json:"problems"`
			KeptBodies   int `json:"kept_bodies_matched"`
			BodiesCutOff int `json:"kept_bodies_cut_short"`
		} `json:"summary"`
		Runs     []*verifiedRun `json:"runs"`
		Problems []finding      `json:"problems"`
		Notes    []finding      `json:"notes"`
		Body     *bodyResult    `json:"body,omitempty"`
	}{Format: "hotline.verify.v1", OK: code == verifyOK, Runs: runs, Problems: problems, Notes: notes, Body: v.body}
	report.Summary.Records, report.Summary.Batches, report.Summary.Runs = len(v.records), len(v.batches), len(v.runs)
	report.Summary.Problems = len(problems)
	report.Summary.KeptBodies, report.Summary.BodiesCutOff = v.matched, v.cut
	data, _ := json.Marshal(report) //nolint:errchkjson // plain strings, numbers and lists always encode
	_, _ = out.Write(export.TerminalSafe(append(data, '\n')))
}

// valueOf is a value read from a line as the attribute the proxy hashed: a
// number with a point or an exponent is a decimal, any other a whole
// number.
func valueOf(v any) attribute.Value {
	switch x := v.(type) {
	case string:
		return attribute.StringValue(x)
	case bool:
		return attribute.BoolValue(x)
	case json.Number:
		if strings.ContainsAny(x.String(), ".eE") {
			f, _ := x.Float64()
			return attribute.Float64Value(f)
		}
		i, _ := x.Int64()
		return attribute.Int64Value(i)
	case []any:
		items := make([]attribute.Value, len(x))
		for i, item := range x {
			items[i] = valueOf(item)
		}
		return attribute.SliceValue(items...)
	}
	// What is left is an object. The file exporter never writes null, so a
	// null someone put in reads as an empty object, and fails its hash.
	x, _ := v.(map[string]any)
	entries := make([]attribute.KeyValue, 0, len(x))
	for name, item := range x {
		entries = append(entries, attribute.KeyValue{Key: attribute.Key(name), Value: valueOf(item)})
	}
	return attribute.MapValue(entries...)
}

// oldBatchFormat is the batch format of the edge builds before every field
// was covered (Peter, 2026-11-25). Their folders are refused by name.
const oldBatchFormat = "hotline.batch.v1"

// recordOf is a line as its hash or signature covers it, with the
// attributes the hash covers: every field the line holds.
func recordOf(line export.Line, attrs []attribute.KeyValue) ledger.Record {
	return ledger.Record{
		Attrs: attrs, At: line.Time, Observed: line.Observed, TraceID: line.TraceID, SpanID: line.SpanID, Flags: line.Flags,
		SeverityNumber: severityNumber(line.Severity), SeverityText: line.Severity, Event: line.Event, Body: line.Body,
		Resource: attributes(line.Resource),
	}
}

// severityNumber is the number OTLP gives a level the proxy writes by its
// name, 0 for a name it never writes.
func severityNumber(text string) int {
	for s := log.SeverityTrace1; s <= log.SeverityFatal4; s++ {
		if s.String() == text {
			return int(s)
		}
	}
	return 0
}

// attributes are values read from a line as the attributes the proxy
// hashed.
func attributes(values map[string]any) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, len(values))
	for name, value := range values {
		attrs = append(attrs, attribute.KeyValue{Key: attribute.Key(name), Value: valueOf(value)})
	}
	return attrs
}

// traceMatches says whether a line's spans have the trace hash want. A span
// id the proxy never writes does not match.
func traceMatches(traceID string, raw []export.LineSpan, want string) bool {
	spans, err := lineSpans(raw)
	return err == nil && ledger.Text(ledger.TraceHash(traceID, spans)) == want
}

// lineSpans are the spans a line holds, as the trace's hash covers them.
func lineSpans(raw []export.LineSpan) ([]ledger.Span, error) {
	spans := make([]ledger.Span, 0, len(raw))
	for _, s := range raw {
		id, err := ledger.ParseSpanID(s.ID)
		if err != nil {
			return nil, err
		}
		parent, err := ledger.ParseSpanID(s.Parent)
		if err != nil {
			return nil, err
		}
		var links []ledger.Link
		for _, link := range s.Links {
			links = append(links, ledger.Link{TraceID: link.TraceID, SpanID: link.SpanID, Flags: link.Flags})
		}
		spans = append(spans, ledger.Span{
			ID: id, Parent: parent, Name: s.Name, Kind: s.Kind,
			Start: s.Start, End: s.End, Status: s.Status, Flags: s.Flags, Links: links, Attrs: attributes(s.Attributes),
		})
	}
	return spans, nil
}

// safe is text from the input, with every character that could change a
// terminal written as an escape.
func safe(s string) string {
	if !strings.ContainsFunc(s, isUnsafe) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if isUnsafe(r) {
			fmt.Fprintf(&b, `\u%04x`, r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isUnsafe(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }

func joinInts(n []int64) string {
	parts := make([]string, len(n))
	for i, x := range n {
		parts[i] = strconv.FormatInt(x, 10)
	}
	return strings.Join(parts, ", ")
}

// thousands writes a count with commas, as 1,025.
func thousands(n int64) string {
	text := strconv.FormatInt(n, 10)
	for i := len(text) - 3; i > 0; i -= 3 {
		text = text[:i] + "," + text[i:]
	}
	return text
}
