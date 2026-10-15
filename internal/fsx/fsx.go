package fsx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// ErrUnsafePath marks a private path that recite refuses to use: a symlink or
// non-directory where a private directory is expected, or a directory owned by
// another user. The CLI maps it to exit 2.
var ErrUnsafePath = errors.New("unsafe private path")

const (
	dirMode  = 0o700
	fileMode = 0o600
)

// MkdirPrivate ensures root and then each of elems below it (root/e1, root/e1/e2, …)
// is a private directory owned by the user with mode 0700. Parents of root are
// created with 0700 if missing but are not otherwise checked, since system
// paths such as /tmp may legitimately be symlinks. It returns the full path, cleaned: "link/"
// or "link/." would make the kernel resolve the link before O_NOFOLLOW sees it.
func MkdirPrivate(root string, elems ...string) (string, error) {
	root = filepath.Clean(root)
	if err := os.MkdirAll(filepath.Dir(root), dirMode); err != nil {
		return "", fmt.Errorf("fsx: create parent of %s: %w", root, err)
	}
	path := root
	if err := ensureDir(path); err != nil {
		return "", err
	}
	for _, e := range elems {
		path = filepath.Join(path, e)
		if err := ensureDir(path); err != nil {
			return "", err
		}
	}
	return path, nil
}

// CheckPrivate checks, without creating or changing anything, that root and each of elems
// below it is a directory owned by the user and not a symlink, the rule MkdirPrivate enforces.
// A missing level ends the check: exists is false and err nil, since there is nothing below it.
func CheckPrivate(root string, elems ...string) (exists bool, err error) {
	path := filepath.Clean(root) // Lstat("link/") reports the link's target, not the link

	for i := -1; i < len(elems); i++ {
		if i >= 0 {
			path = filepath.Join(path, elems[i])
		}
		fi, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("fsx: stat %s: %w", path, err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return false, fmt.Errorf("fsx: %s is a symlink or not a directory: %w", path, ErrUnsafePath)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
			return false, fmt.Errorf("fsx: %s is owned by uid %d, not by you: %w", path, st.Uid, ErrUnsafePath)
		}
	}
	return true, nil
}

// ensureDir creates path as 0700 if absent, opens it without following a
// symlink, and checks owner and mode on the descriptor so the check and the
// fchmod apply to the same inode.
func ensureDir(path string) error {
	if err := syscall.Mkdir(path, dirMode); err != nil && !errors.Is(err, syscall.EEXIST) {
		return fmt.Errorf("fsx: mkdir %s: %w", path, err)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
			return fmt.Errorf("fsx: %s is a symlink or not a directory: %w", path, ErrUnsafePath)
		}
		return fmt.Errorf("fsx: open %s: %w", path, err)
	}
	defer func() { _ = syscall.Close(fd) }() // read-only descriptor; close cannot lose data
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return fmt.Errorf("fsx: stat %s: %w", path, err)
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("fsx: %s is owned by uid %d, not by you: %w", path, st.Uid, ErrUnsafePath)
	}
	// Exactly 0700: wider leaks, and narrower (0500, 0600) would leave recite unable to write or
	// search the directory it was just promised.
	if st.Mode&0o777 != dirMode {
		if err := syscall.Fchmod(fd, dirMode); err != nil {
			return fmt.Errorf("fsx: set %s to 0700: %w", path, err)
		}
	}
	return nil
}

// CreatePrivate opens path for writing with mode 0600, truncating it. It refuses to follow a
// symlink at path, and checks the opened descriptor before changing anything: it must be a
// regular file owned by the user with one link, so a FIFO cannot block the open (O_NONBLOCK)
// and neither a hard link nor another user's file is truncated or chmodded. An existing wider
// file is tightened through fchmod.
func CreatePrivate(path string) (*os.File, error) {
	f, err := openPrivate(path, 0)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close() // already failing; the truncate error is the one to report
		return nil, fmt.Errorf("fsx: truncate %s: %w", path, err)
	}
	return f, nil
}

// AppendPrivate opens path for appending with mode 0600, with CreatePrivate's checks: an
// existing file is kept, and tightened to 0600 if it was wider.
func AppendPrivate(path string) (*os.File, error) { return openPrivate(path, os.O_APPEND) }

// openPrivate opens path write-only, creating it 0600, and checks the descriptor before
// changing anything (CreatePrivate).
func openPrivate(path string, flags int) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|flags, fileMode)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("fsx: %s is a symlink: %w", path, ErrUnsafePath)
		}
		if errors.Is(err, syscall.ENXIO) {
			return nil, fmt.Errorf("fsx: %s is not a regular file: %w", path, ErrUnsafePath)
		}
		return nil, fmt.Errorf("fsx: create %s: %w", path, err)
	}
	fail := func(err error) (*os.File, error) {
		_ = f.Close() // already failing; err is the one to report
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		return fail(fmt.Errorf("fsx: stat %s: %w", path, err))
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.Mode().IsRegular():
		return fail(fmt.Errorf("fsx: %s is not a regular file: %w", path, ErrUnsafePath))
	case ok && int(st.Uid) != os.Getuid():
		return fail(fmt.Errorf("fsx: %s is owned by uid %d, not by you: %w", path, st.Uid, ErrUnsafePath))
	case ok && st.Nlink != 1:
		return fail(fmt.Errorf("fsx: %s has %d links: %w", path, st.Nlink, ErrUnsafePath))
	}
	if err := f.Chmod(fileMode); err != nil {
		return fail(fmt.Errorf("fsx: chmod %s: %w", path, err))
	}
	return f, nil
}

// WritePrivate writes data to path with mode 0600 (see CreatePrivate).
func WritePrivate(path string, data []byte) error {
	f, err := CreatePrivate(path)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsx: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("fsx: close %s: %w", path, err)
	}
	return nil
}
