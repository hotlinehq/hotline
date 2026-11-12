package export

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"hotline-proxy/internal/config"
)

// Console is where the console exporter prints, from the world outside
// the proxy: standard output, whether it is a terminal and how wide, and
// the environment, for NO_COLOR and COLUMNS.
type Console struct {
	Out io.Writer
	// Terminal gives the terminal's width in columns, and false when the
	// output is not a terminal.
	Terminal  func() (int, bool)
	LookupEnv func(string) (string, bool)
	// JSON prints each call as one line of JSON, the line a file exporter
	// writes, for --format json (Peter, 2026-11-12).
	JSON bool
}

// consoleQueueSize is how many calls wait for the console at most.
const consoleQueueSize = 1000

// consoleDestination prints each call as two lines, or three for an
// invalid call, like mitmdump (Peter, 2026-11-11).
type consoleDestination struct {
	fileDestination
	out   Console
	color bool
}

// newConsole starts the console's queue. Colour is used when the config
// says always, or says auto and the output is a terminal and NO_COLOR is
// not set.
func newConsole(e *config.ConsoleExporter, out Console, setup queueSetup) *consoleDestination {
	c := &consoleDestination{out: out, color: coloured(e, out)}
	write := func(_ context.Context, calls [][]byte) error {
		_, err := out.Out.Write(bytes.Join(calls, nil))
		return err
	}
	c.queue = newQueue("calls", write, func(context.Context) error { return nil }, setup)
	c.up, c.place = setup.up, attribute.NewSet(attribute.String("hotline.exporter", "console"))
	return c
}

// coloured says whether the console's lines are coloured: when the config
// says always, or says auto and the output is a terminal and NO_COLOR is
// not set.
func coloured(e *config.ConsoleExporter, out Console) bool {
	_, terminal := out.Terminal()
	noColor, _ := out.LookupEnv("NO_COLOR")
	return e.Color == "always" || e.Color == "auto" && terminal && noColor == ""
}

// width is the terminal's width, else COLUMNS, else 80, as mitmdump
// reads it.
func (c *consoleDestination) width() int {
	if width, ok := c.out.Terminal(); ok {
		return width
	}
	columns, _ := c.out.LookupEnv("COLUMNS")
	if width, err := strconv.Atoi(columns); err == nil && width > 0 {
		return width
	}
	return 80
}

// The terminal's codes for the styles the console uses.
const (
	bold    = "1"
	dim     = "2"
	red     = "31"
	green   = "32"
	yellow  = "33"
	magenta = "35"
	cyan    = "36"
)

// consoleLines writes one call: the request, the reply under it and, for
// an invalid call, what the checks found.
type consoleLines struct {
	b     bytes.Buffer
	color bool
}

// style writes text in the styles, when colour is on.
func (l *consoleLines) style(text string, codes ...string) {
	if !l.color || !slices.ContainsFunc(codes, func(c string) bool { return c != "" }) {
		l.b.WriteString(text)
		return
	}
	l.b.WriteString("\x1b[" + strings.Join(codes, ";") + "m" + text + "\x1b[0m")
}

// word writes a space and the text in the styles, or nothing when there
// is no text.
func (l *consoleLines) word(text string, codes ...string) {
	if text == "" {
		return
	}
	l.b.WriteString(" ")
	l.style(text, codes...)
}

// lines writes the call. Every value that came with the call is escaped,
// so it cannot change the terminal.
func (c *consoleDestination) lines(call Call) []byte {
	l := &consoleLines{color: c.color}
	var (
		id, method, failure, checked, host, address string
		status, size, took                          int64 = 0, -1, 0
		found                                       []string
	)
	for _, kv := range call.Attrs {
		switch kv.Key {
		case "hotline.integration.id":
			id = printable(kv.Value.AsString())
		case semconv.ServerAddressKey:
			host = kv.Value.AsString()
		case semconv.URLFullKey:
			address = kv.Value.AsString()
		case semconv.HTTPRequestMethodKey:
			method = printable(kv.Value.AsString())
		case semconv.HTTPResponseStatusCodeKey:
			status = kv.Value.AsInt64()
		case semconv.ErrorTypeKey:
			failure = printable(kv.Value.AsString())
		case semconv.HTTPResponseBodySizeKey:
			size = kv.Value.AsInt64()
		case "hotline.timing.total_us":
			took = kv.Value.AsInt64()
		case "hotline.schema.status":
			checked = kv.Value.AsString()
		case "hotline.schema.request.errors":
			found = append(found, side("request", kv.Value))
		case "hotline.schema.response.errors":
			found = append(found, side("reply", kv.Value))
		}
	}
	at := call.At.Local().Format("15:04:05.000")
	l.style(at, dim)
	l.b.WriteString(" ")
	l.style(id, cyan)
	l.b.WriteString(": ")
	l.style(method, bold, methodColour(method))
	l.b.WriteString(" ")
	// The address as the record has it, with its secrets hidden, or else
	// the host: the console never shows more than the record holds (Peter,
	// 2026-11-12).
	l.style(cut(printable(cmp.Or(address, host)), c.width()), bold)
	l.b.WriteString("\n")

	// "<< " ends just before the method, as in mitmdump.
	under := strings.Repeat(" ", utf8.RuneCountInString(at)+1+utf8.RuneCountInString(id)+2)
	l.b.WriteString(under[3:])
	l.style("<<", bold)
	reply := ""
	if status > 0 {
		reply = strconv.FormatInt(status, 10) + " " + http.StatusText(int(status))
	}
	l.word(reply, bold, statusColour(status))
	l.word(failure, bold, red)
	shown := ""
	if size >= 0 {
		shown = byteSize(size)
	}
	l.word(shown)
	l.word(strconv.FormatFloat(float64(took)/1000, 'f', 1, 64)+" ms", slow(time.Duration(took)*time.Microsecond)...)
	l.word(checked, checkColour(checked))
	l.b.WriteString("\n")

	for i, f := range found {
		if i == 0 {
			l.b.WriteString(under)
		} else {
			l.style("; ", dim)
		}
		name, rest, _ := strings.Cut(f, ":")
		l.style(name, red)
		l.style(":"+rest, dim)
	}
	if len(found) > 0 {
		l.b.WriteString("\n")
	}
	return l.b.Bytes()
}

// terminalSafe is a JSON line with every character that could change the
// terminal escaped as \uXXXX. JSON itself escapes only the characters
// below 0x20, a quote and a backslash, and those characters can only sit
// inside a JSON string, where the escape means the same. The line is
// returned as it is when nothing needs escaping.
func terminalSafe(line []byte) []byte {
	safe := true
	for _, r := range string(line) {
		if unsafeInLine(r) {
			safe = false
			break
		}
	}
	if safe {
		return line
	}
	out := make([]byte, 0, len(line)+16)
	for _, r := range string(line) {
		if !unsafeInLine(r) {
			out = utf8.AppendRune(out, r)
			continue
		}
		if r > 0xffff {
			hi, lo := utf16.EncodeRune(r)
			out = fmt.Appendf(out, `\u%04x\u%04x`, hi, lo)
			continue
		}
		out = fmt.Appendf(out, `\u%04x`, r)
	}
	return out
}

// unsafeInLine is a character that could change the terminal, other than
// the line break that ends a JSON line.
func unsafeInLine(r rune) bool { return r != '\n' && unsafe(r) }

// side is what the checks found on one side, written "part place rule
// expected" and joined by "; ", never a value from the call.
func side(name string, results attribute.Value) string {
	all := make([]string, 0, len(results.AsSlice()))
	for _, r := range results.AsSlice() {
		var words []string
		fields := map[attribute.Key]string{}
		for _, kv := range r.AsMap() {
			fields[kv.Key] = kv.Value.AsString()
		}
		for _, key := range []attribute.Key{"part", "place", "rule", "expected"} {
			if fields[key] != "" {
				words = append(words, fields[key])
			}
		}
		all = append(all, printable(strings.Join(words, " ")))
	}
	return name + ": " + strings.Join(all, "; ")
}

// methodColour is mitmdump's: GET green, DELETE red, every other magenta.
func methodColour(method string) string {
	switch method {
	case "GET":
		return green
	case "DELETE":
		return red
	default:
		return magenta
	}
}

// statusColour is green for 2xx, magenta for 3xx, yellow for 4xx and red
// for the rest: 5xx and the proxy's own failures.
func statusColour(status int64) string {
	switch status / 100 {
	case 2:
		return green
	case 3:
		return magenta
	case 4:
		return yellow
	default:
		return red
	}
}

// checkColour is green for valid, red for invalid and dim for skipped.
func checkColour(status string) string {
	switch status {
	case "valid":
		return green
	case "invalid":
		return red
	default:
		return dim
	}
}

// slow is yellow for a call that took more than a second.
func slow(took time.Duration) []string {
	if took > time.Second {
		return []string{yellow}
	}
	return nil
}

// byteSize is a size in bytes, KiB or MiB.
func byteSize(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return strconv.FormatFloat(v, 'f', min(i, 1), 64) + " " + units[i]
}

// cut shortens an address to the terminal's width less 25 characters,
// and at least 50, as mitmdump does.
func cut(address string, width int) string {
	most := max(width-25, 50)
	runes := []rune(address)
	if len(runes) <= most {
		return address
	}
	return string(runes[:most]) + "…"
}

// printable escapes every character that could change the terminal: the
// control and format characters, and bytes that are not UTF-8.
func printable(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == utf8.RuneError && !strings.HasPrefix(s[i:], "�"):
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case unsafe(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
