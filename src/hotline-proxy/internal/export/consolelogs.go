package export

import (
	"context"
	"fmt"
	"log/slog"

	"hotline-proxy/internal/config"
)

// consoleLogs writes the proxy's own logs as plain lines, for a person
// watching the console (Peter, 2026-11-12):
//
//	07:48:03.662 WARN  config warning: there is no listen.admin address
//	07:48:03.662 INFO  hotline proxy started  address=127.0.0.1:8080
//
// Which levels are written, and With and WithGroup, are the handler's it
// replaces; the proxy never uses With or WithGroup.
type consoleLogs struct {
	slog.Handler
	out   Console
	color bool
}

// ConsoleLogs is a handler that writes plain lines to out in place of
// next, coloured as the console exporter's lines are.
func ConsoleLogs(e *config.ConsoleExporter, out Console, next slog.Handler) slog.Handler {
	return consoleLogs{Handler: next, out: out, color: coloured(e, out)}
}

// Handle writes one line: the time, the level, the message, a reason
// after a colon, then every other field as name=value. Every value is
// escaped, as on the console.
func (h consoleLogs) Handle(_ context.Context, r slog.Record) error {
	l := &consoleLines{color: h.color}
	l.style(r.Time.Local().Format("15:04:05.000"), dim)
	l.b.WriteString(" ")
	// DEBUG is -4, INFO 0, WARN 4 and ERROR 8.
	l.style(fmt.Sprintf("%-5s", r.Level.String()), [...]string{dim, "", yellow, red}[min(max(int(r.Level)/4+1, 0), 3)])
	l.b.WriteString(" " + printable(r.Message))
	var fields consoleLines
	fields.color = h.color
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "reason" {
			l.b.WriteString(": " + printable(a.Value.String()))
			return true
		}
		fields.b.WriteString(" ")
		fields.style(printable(a.Key)+"=", dim)
		fields.b.WriteString(printable(a.Value.String()))
		return true
	})
	if fields.b.Len() > 0 {
		l.b.WriteString(" ")
		l.b.Write(fields.b.Bytes())
	}
	l.b.WriteString("\n")
	_, err := h.out.Out.Write(l.b.Bytes())
	return err
}
