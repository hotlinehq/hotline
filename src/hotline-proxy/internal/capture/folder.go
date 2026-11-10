package capture

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// The reasons a socket folder is refused, each wrapped with its path.
var (
	// ErrNotFolder is a path that is not a folder, or is a link to one.
	ErrNotFolder = errors.New("capture: not a folder")
	// ErrWrongOwner is a folder owned by another user.
	ErrWrongOwner = errors.New("capture: the socket folder is not owned by the current user")
	// ErrWrongMode is a folder with a mode other than 0700.
	ErrWrongMode = errors.New("capture: the socket folder does not have mode 700")
)

// Folder is the private folder the capture sockets live in: a random name,
// open only to the user.
type Folder struct {
	dir string
}

// NewFolder makes the folder with mode 0700 and checks it, in the user's own
// temporary folder.
func NewFolder() (*Folder, error) {
	// MkdirTemp picks a random name and makes the folder with mode 0700.
	dir, err := os.MkdirTemp("", "hotline-capture-")
	if err != nil {
		return nil, fmt.Errorf("capture: making the socket folder: %w", err)
	}
	if err := CheckFolder(dir, os.Getuid()); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &Folder{dir: dir}, nil
}

// Socket is the path of the socket in the folder.
func (f *Folder) Socket() string {
	return filepath.Join(f.dir, "capture.sock")
}

// Close removes the folder and the socket in it.
func (f *Folder) Close() error {
	return os.RemoveAll(f.dir)
}

// CheckFolder says whether path is a folder owned by the user with number
// uid and open to no one else: mode exactly 0700. A link to a folder is
// refused, since its target could be changed by others.
func CheckFolder(path string, uid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("capture: the socket folder: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrNotFolder, path)
	}
	owner, known := info.Sys().(*syscall.Stat_t)
	if !known || int(owner.Uid) != uid { //nolint:gosec // user numbers are small
		return fmt.Errorf("%w: %s", ErrWrongOwner, path)
	}
	if mode := info.Mode(); mode.Perm() != 0o700 || mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return fmt.Errorf("%w: %s has mode %o", ErrWrongMode, path, mode.Perm())
	}
	return nil
}
