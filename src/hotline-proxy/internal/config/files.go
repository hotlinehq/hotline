package config

import (
	"os"
	"path/filepath"
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
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".hotline-check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
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
