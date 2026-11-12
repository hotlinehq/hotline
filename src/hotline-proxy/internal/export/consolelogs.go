package export

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"hotline-proxy/internal/config"
)

// consoleLogs writes the proxy's own logs as plain lines, for a person
// watching the console (Peter, 2026-11-12):
//
//	07:48:03.662 WARN  config warning: there is no listen.admin address
//	07:48:03.662 INFO  hotline proxy started  address=127.0.0.1:8080
//
// Which levels are written is the handler's it replaces. A logger made with
// With or WithGroup keeps writing plain lines, with its fields.
type consoleLogs struct {
	next  slog.Handler
	out   Console
	color bool
	// mu lets one line be written at a time, as slog's own handlers do,
	// shared by every logger made from this one.
	mu *sync.Mutex
	// fields are the fields given to With, already written; group is the
	// name of the WithGroup groups, ending with a dot.
	fields string
	group  string
}

// ConsoleLogs is a handler that writes plain lines to out in place of
// next, coloured as the console exporter's lines are.
func ConsoleLogs(e *config.ConsoleExporter, out Console, next slog.Handler) slog.Handler {
	return &consoleLogs{next: next, out: out, color: coloured(e, out), mu: &sync.Mutex{}}
}

func (h *consoleLogs) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *consoleLogs) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.next = h.next.WithAttrs(attrs)
	var fields consoleLines
	fields.color = h.color
	for _, a := range attrs {
		h.field(&fields, h.group, a)
	}
	c.fields += fields.b.String()
	return &c
}

func (h *consoleLogs) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.next = h.next.WithGroup(name)
	c.group += name + "."
	return &c
}

// Handle writes one line: the time, the level, the message, a reason or
// problem after a colon, then every other field as name=value. Every value is
// escaped, as on the console, and quoted when it is empty or holds a space,
// an equals sign or a quote, so it cannot be read as more fields.
func (h *consoleLogs) Handle(_ context.Context, r slog.Record) error {
	l := &consoleLines{color: h.color}
	l.style(r.Time.Local().Format("15:04:05.000"), dim)
	l.b.WriteString(" ")
	l.style(fmt.Sprintf("%-5s", r.Level.String()), levelColour(r.Level))
	l.b.WriteString(" " + printable(r.Message))
	var fields consoleLines
	fields.color = h.color
	fields.b.WriteString(h.fields)
	r.Attrs(func(a slog.Attr) bool {
		if h.group == "" && (a.Key == "reason" || a.Key == "problem") {
			l.b.WriteString(": " + printable(a.Value.String()))
			return true
		}
		h.field(&fields, h.group, a)
		return true
	})
	if fields.b.Len() > 0 {
		l.b.WriteString(" ")
		l.b.Write(fields.b.Bytes())
	}
	l.b.WriteString("\n")
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.out.Out.Write(l.b.Bytes())
	return err
}

// field writes one field as a space and name=value. A group's fields are
// named after the group: group.name=value.
func (h *consoleLogs) field(l *consoleLines, group string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		if a.Key != "" {
			group += a.Key + "."
		}
		for _, inner := range a.Value.Group() {
			h.field(l, group, inner)
		}
		return
	}
	if a.Equal(slog.Attr{}) {
		return
	}
	l.b.WriteString(" ")
	l.style(printable(group+a.Key)+"=", dim)
	l.b.WriteString(logValue(a.Value.String()))
}

// logValue is a value as written in a log line: as it is when that is
// safe, else in quotes with every character that could change the
// terminal escaped.
func logValue(v string) string {
	if v == "" || strings.ContainsAny(v, ` ="\`) {
		return strconv.Quote(v)
	}
	if p := printable(v); p != v {
		return strconv.Quote(v)
	}
	return v
}

// levelColour is dim below INFO, none from INFO, yellow from WARN and red
// from ERROR, so a level between two names gets the lower one's colour.
func levelColour(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return dim
	case level < slog.LevelWarn:
		return ""
	case level < slog.LevelError:
		return yellow
	default:
		return red
	}
}

// unsafe is a character that could change the terminal: a control
// character, or a format character such as a right-to-left mark, which
// reorders the text after it.
func unsafe(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}
