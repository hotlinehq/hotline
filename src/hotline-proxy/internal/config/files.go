package config

import (
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// The file exporter's defaults (Peter, 2026-11-11).
const (
	defaultMaxFileSize = 100 << 20
	defaultMaxFiles    = 10
	defaultFileQueue   = 256 << 20
	defaultMaxBody     = 1 << 20
)

// fileExporter reads one file exporter. The folder is made when it is
// missing and must take a file, so a folder the proxy cannot write to is
// refused at start, not found out at the first call. Two file exporters
// may not share a folder, since their lines and rotations would mix.
func (l *loader) fileExporter(f fileExporterFile, before []FileExporter) FileExporter {
	label := "file exporter " + f.Path
	e := FileExporter{
		MaxFileSize: l.size(label+": maxFileSize", f.MaxFileSize, defaultMaxFileSize),
		MaxFiles:    defaultMaxFiles,
		Gzip:        true,
		QueueMemory: l.size(label+": queueMemory", f.QueueMemory, defaultFileQueue),
	}
	if f.MaxFiles != nil {
		e.MaxFiles = *f.MaxFiles
	}
	if e.MaxFiles < 1 {
		l.problem("%s: maxFiles must be at least 1", label)
	}
	switch f.Compression {
	case "", "gzip":
	case "none":
		e.Gzip = false
	default:
		l.problem("%s: compression must be gzip or none", label)
	}
	if f.Path == "" {
		l.problem("a file exporter needs a path, the folder it writes to")
		return e
	}
	e.Path = filepath.Clean(l.path(f.Path))
	for _, other := range before {
		if other.Path == e.Path {
			l.problem("two file exporters write to the folder %s", f.Path)
		}
	}
	if err := writable(e.Path); err != nil {
		l.problem("%s: the folder cannot be written to: %v", label, err)
	}
	return e
}

// writable makes the folder if it is missing and checks that a file can be
// made in it.
func writable(dir string) error {
	// A folder that cannot be made shows in the check below.
	_ = os.MkdirAll(dir, 0o750)
	f, err := os.CreateTemp(dir, ".hotline-check-*")
	if err != nil {
		return err
	}
	_ = f.Close()
	return os.Remove(f.Name())
}

// content reads an integration's content and maxBodySize. Set says the
// integration wrote either.
func (l *loader) content(label, content, maxBody string) (c Content, set bool) {
	c.MaxBody = l.size(label+": maxBodySize", maxBody, defaultMaxBody)
	switch content {
	case "":
	case "records":
	case "full":
		c.Full = true
	default:
		l.problem("%s: content must be records or full", label)
	}
	return c, content != "" || maxBody != ""
}

// ConsoleExporter prints every call to standard output.
type ConsoleExporter struct {
	// Color is auto, always or never: auto colours the lines only on a
	// terminal, and never when NO_COLOR is set.
	Color string
}

// consoleExporterFile is the console exporter as the config writes it.
type consoleExporterFile struct {
	Color string `yaml:"color"`
}

// console reads the console exporter. There is one at most, since two
// would print the same lines (Peter, 2026-11-11). The key alone switches
// it on: "console:" with nothing after it means every setting at its
// default, as in the OpenTelemetry Collector (Peter, 2026-11-13). So the
// config keeps it as a YAML node, which is set whenever the key is there,
// even with nothing after it, and reads its settings here.
func (l *loader) console(node yaml.Node, before *ConsoleExporter) *ConsoleExporter {
	if before != nil {
		l.problem("there may be only one console exporter")
	}
	var f consoleExporterFile
	if err := node.Decode(&f); err != nil {
		l.problem("console exporter: write its settings as color: auto, or nothing for the defaults")
	}
	for i := 0; node.Kind == yaml.MappingNode && i+1 < len(node.Content); i += 2 {
		if key := node.Content[i]; key.Value != "color" {
			l.warnings = append(l.warnings, fmt.Sprintf("line %d: field %s is not a setting and is ignored", key.Line, key.Value))
		}
	}
	e := &ConsoleExporter{Color: f.Color}
	switch f.Color {
	case "":
		e.Color = "auto"
	case "auto", "always", "never":
	default:
		l.problem("console exporter: color must be auto, always or never")
	}
	return e
}

// btoi is 1 for true and 0 for false.
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
