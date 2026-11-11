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
	file *os.File
	size int64
	// pending is the lines of one write, kept between writes.
	pending []byte
}

// openFileWriter takes the folder's lock and opens the current file, to
// add to what is in it.
func openFileWriter(e config.FileExporter) (*fileWriter, error) {
	if err := os.MkdirAll(e.Path, 0o750); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(e.Path, lockFile), os.O_CREATE|os.O_RDWR, 0o640) //nolint:gosec // the config names the folder
	if err != nil {
		return nil, err
	}
	if err := lockExclusive(lock); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("%w: %s", ErrFolderTaken, e.Path)
	}
	w := &fileWriter{e: e, lock: lock}
	if err := w.open(); err != nil {
		_ = w.close()
		return nil, err
	}
	return w, nil
}

// open opens the current file, making the folder again if it went away.
func (w *fileWriter) open() error {
	if err := os.MkdirAll(w.e.Path, 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(w.e.Path, currentFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640) //nolint:gosec // the config names the folder
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.file, w.size = f, info.Size()
	return nil
}

// write adds the lines, rotating first whenever the next line would take
// the file past its size. A failed write closes the file, so the next one
// opens it again. So does a current file that is gone or was moved away,
// for example when someone deleted the folder or rotated the file
// themselves: writing on would lose the lines in a file nobody can see.
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
	w.pending = w.pending[:0]
	for _, line := range lines {
		if w.size+int64(len(w.pending)) > 0 && w.size+int64(len(w.pending)+len(line)) > w.e.MaxFileSize {
			if err := w.flush(); err != nil {
				return err
			}
			if err := w.rotate(); err != nil {
				return err
			}
		}
		w.pending = append(w.pending, line...)
	}
	return w.flush()
}

// inPlace says the open file is still the current file in the folder.
func (w *fileWriter) inPlace() bool {
	open, err := w.file.Stat()
	if err != nil {
		return false
	}
	named, err := os.Stat(filepath.Join(w.e.Path, currentFile))
	return err == nil && os.SameFile(open, named)
}

// flush writes the pending lines in one go.
func (w *fileWriter) flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	n, err := w.file.Write(w.pending)
	w.size += int64(n)
	w.pending = w.pending[:0]
	if err != nil {
		_ = w.file.Close()
		w.file = nil
	}
	return err
}

// rotate renames the current file after the time, gzips it when set,
// deletes the oldest rotated files beyond the count kept, and opens a new
// current file.
func (w *fileWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		w.file = nil
		return err
	}
	w.file = nil
	stamp := time.Now().UTC().Format("20060102T150405.000000Z")
	name := filepath.Join(w.e.Path, rotatedHead+stamp+rotatedTail)
	for n := 2; exists(name) || exists(name+".gz"); n++ {
		name = filepath.Join(w.e.Path, fmt.Sprintf("%s%s-%d%s", rotatedHead, stamp, n, rotatedTail))
	}
	if err := os.Rename(filepath.Join(w.e.Path, currentFile), name); err != nil {
		return err
	}
	if w.e.Gzip {
		if err := gzipFile(name); err != nil {
			return err
		}
	}
	if err := w.prune(); err != nil {
		return err
	}
	return w.open()
}

func exists(name string) bool {
	_, err := os.Lstat(name)
	return err == nil
}

// gzipFile compresses a rotated file next to it and removes the plain one.
// The gzipped file appears only once it is whole.
func gzipFile(name string) error {
	in, err := os.Open(name) //nolint:gosec // a file the writer itself renamed
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	partial := name + ".gz.partial"
	out, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640) //nolint:gosec // next to the file above
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(out)
	_, err = io.Copy(zw, in)
	err = errors.Join(err, zw.Close(), out.Close())
	if err == nil {
		err = os.Rename(partial, name+".gz")
	}
	if err != nil {
		_ = os.Remove(partial)
		return err
	}
	return os.Remove(name)
}

// prune deletes the oldest rotated files beyond the count kept. Their names
// hold the time they were rotated, so the oldest sort first.
func (w *fileWriter) prune() error {
	entries, err := os.ReadDir(w.e.Path)
	if err != nil {
		return err
	}
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

// close closes the current file and lets go of the folder.
func (w *fileWriter) close() error {
	var err error
	if w.file != nil {
		err = w.file.Close()
		w.file = nil
	}
	if w.lock != nil {
		err = errors.Join(err, w.lock.Close())
		w.lock = nil
	}
	return err
}
