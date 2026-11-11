package export

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"hotline-proxy/internal/config"
)

// ErrFolderTaken is a file exporter's folder that another proxy writes to.
var ErrFolderTaken = errors.New("export: the folder is in use by another proxy")

// The names in a file exporter's folder.
const (
	currentFile = "calls.jsonl"
	rotatedHead = "calls-"
	rotatedTail = ".jsonl"
	lockFile    = ".lock"
)

// fileWriter writes call lines to calls.jsonl in one folder, and starts a
// new file when the current one would pass its size: the old one is renamed
// after the time, gzipped, and the oldest beyond the count kept are
// deleted. A line is never split across files. The current file is never
// compressed, so it can be followed as it grows (Peter, 2026-11-11).
type fileWriter struct {
	e config.FileExporter
	// lock is held for as long as the writer lives, so a second proxy
	// cannot write to the same folder.
	lock *os.File
	// file is the current file, nil until the first write and after a
	// rotation that failed.
	file *os.File
	size int64
	// pending is the lines of one write, kept between writes.
	pending []byte
}

// openFileWriter takes the folder's lock. The config has made the folder.
// The current file is opened by the first write.
func openFileWriter(e config.FileExporter) (*fileWriter, error) {
	lock, err := os.OpenFile(filepath.Join(e.Path, lockFile), os.O_CREATE|os.O_RDWR, 0o640) //nolint:gosec // the config names the folder
	if err != nil {
		return nil, err
	}
	if err := lockExclusive(lock); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("%w: %s", ErrFolderTaken, e.Path)
	}
	return &fileWriter{e: e, lock: lock}, nil
}

// write adds the lines, rotating first whenever the next line would take
// the file past its size. A current file that is gone or was moved away,
// for example when someone deleted the folder or rotated the file
// themselves, is opened again: writing on would lose the lines in a file
// nobody can see. The lines of a write that failed are lost, and counted
// by the queue.
func (w *fileWriter) write(lines [][]byte) error {
	if w.file != nil && !w.inPlace() {
		_ = w.file.Close()
		w.file = nil
	}
	if w.file == nil {
		if err := w.open(); err != nil {
			return err
		}
	}
	var errs error
	for _, line := range lines {
		waiting := w.size + int64(len(w.pending))
		if waiting > 0 && waiting+int64(len(line)) > w.e.MaxFileSize {
			errs = errors.Join(errs, w.flush(), w.rotate())
		}
		w.pending = append(w.pending, line...)
	}
	return errors.Join(errs, w.flush())
}

// open opens the current file, to add to it, making the folder again if it
// went away.
func (w *fileWriter) open() error {
	// A folder that cannot be made shows in the open below.
	_ = os.MkdirAll(w.e.Path, 0o750)
	f, err := os.OpenFile(filepath.Join(w.e.Path, currentFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640) //nolint:gosec // the config names the folder
	if err != nil {
		return err
	}
	w.file = f
	// A file just opened always knows its end.
	w.size, _ = f.Seek(0, io.SeekEnd)
	return nil
}

// inPlace says the open file is still the current file in the folder.
func (w *fileWriter) inPlace() bool {
	open, _ := w.file.Stat()
	named, err := os.Stat(filepath.Join(w.e.Path, currentFile))
	return err == nil && os.SameFile(open, named)
}

// flush writes the pending lines in one go. Without a current file, after
// a rotation that failed, the write fails and the lines are lost.
func (w *fileWriter) flush() error {
	n, err := w.file.Write(w.pending)
	w.size += int64(n)
	w.pending = w.pending[:0]
	return err
}

// rotate renames the current file after the time, gzips it when set,
// deletes the oldest rotated files beyond the count kept, and opens a new
// current file. The time is to the nanosecond, so two rotations never
// share a name.
func (w *fileWriter) rotate() error {
	current := filepath.Join(w.e.Path, currentFile)
	name := filepath.Join(w.e.Path, rotatedHead+time.Now().UTC().Format("20060102T150405.000000000Z")+rotatedTail)
	return steps(
		func() error {
			err := w.file.Close()
			w.file = nil
			return err
		},
		func() error { return os.Rename(current, name) },
		func() error { return w.compress(name) },
		w.prune,
		w.open,
	)
}

// steps runs each step in turn, until one fails.
func steps(all ...func() error) error {
	for _, step := range all {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// compress gzips a rotated file, when the exporter says so.
func (w *fileWriter) compress(name string) error {
	if !w.e.Gzip {
		return nil
	}
	return gzipFile(name)
}

// gzipFile compresses a rotated file next to it and removes the plain one.
// The gzipped file appears only once it is whole.
func gzipFile(name string) error {
	var in, out *os.File
	partial := name + ".gz.partial"
	err := steps(
		func() (err error) {
			in, err = os.Open(name) //nolint:gosec // a file the writer itself renamed
			return err
		},
		func() (err error) {
			out, err = os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640) //nolint:gosec // next to the file above
			return err
		},
		func() error {
			zw := gzip.NewWriter(out)
			_, err := io.Copy(zw, in)
			return errors.Join(err, zw.Close(), out.Close())
		},
		func() error { return os.Rename(partial, name+".gz") },
		func() error { return os.Remove(name) },
	)
	// Closing a file that never opened, or removing one that is gone,
	// changes nothing.
	_ = in.Close()
	_ = os.Remove(partial)
	return err
}

// prune deletes the oldest rotated files beyond the count kept. Their names
// hold the time they were rotated, so the oldest sort first. A folder that
// cannot be read is pruned at the next rotation.
func (w *fileWriter) prune() error {
	entries, _ := os.ReadDir(w.e.Path)
	var rotated []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, rotatedHead) && (strings.HasSuffix(name, rotatedTail) || strings.HasSuffix(name, rotatedTail+".gz")) {
			rotated = append(rotated, name)
		}
	}
	slices.Sort(rotated)
	var errs []error
	for len(rotated) > w.e.MaxFiles {
		errs = append(errs, os.Remove(filepath.Join(w.e.Path, rotated[0])))
		rotated = rotated[1:]
	}
	return errors.Join(errs...)
}

// close closes the current file, if one is open, and lets go of the
// folder. Every line was written with its own write, so closing loses
// nothing.
func (w *fileWriter) close() error {
	_ = w.file.Close()
	return w.lock.Close()
}
