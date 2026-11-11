package export

import "hotline-proxy/internal/config"

// What the tests in export_test may reach of the file writer: open one,
// write lines, and rotate it at a moment the test picks, to pin what it
// does when the folder changes under it.

// TestFileWriter writes call lines to one folder.
type TestFileWriter struct{ w *fileWriter }

// OpenTestFileWriter takes the folder's lock, rotating past maxSize and
// keeping maxFiles rotated files, without gzip.
func OpenTestFileWriter(dir string, maxSize int64, maxFiles int) (TestFileWriter, error) {
	w, err := openFileWriter(config.FileExporter{Path: dir, MaxFileSize: maxSize, MaxFiles: maxFiles})
	return TestFileWriter{w}, err
}

// Write adds lines, each ending in a line break.
func (t TestFileWriter) Write(lines ...string) error {
	written := make([][]byte, len(lines))
	for i, line := range lines {
		written[i] = []byte(line + "\n")
	}
	return t.w.write(written)
}

// Rotate starts a new file now.
func (t TestFileWriter) Rotate() error { return t.w.rotate() }

// Close closes the file and lets go of the folder.
func (t TestFileWriter) Close() error { return t.w.close() }
