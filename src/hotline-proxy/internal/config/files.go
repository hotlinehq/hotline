package config

import (
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
// would print the same lines (Peter, 2026-11-11).
func (l *loader) console(f consoleExporterFile, before *ConsoleExporter) *ConsoleExporter {
	if before != nil {
		l.problem("there may be only one console exporter")
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

// emptyConsoles fills in each exporter written "console:" with nothing
// after it. YAML reads that as empty, which leaves the exporter looking
// like none at all; it means the same as "console: {}", every option at
// its default (Peter, 2026-11-13). The data is already known to be valid
// YAML, so this second read cannot fail.
func emptyConsoles(data []byte, f *file) {
	var raw struct {
		Exporters []map[string]yaml.Node `yaml:"exporters"`
	}
	_ = yaml.Unmarshal(data, &raw)
	for i, e := range raw.Exporters {
		if console, ok := e["console"]; ok && console.ShortTag() == "!!null" && i < len(f.Exporters) {
			f.Exporters[i].Console = &consoleExporterFile{}
		}
	}
}

// btoi is 1 for true and 0 for false.
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
