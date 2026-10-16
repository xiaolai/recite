package fsx

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// createRetries bounds the retries of a create that macOS failed spuriously (Dir.OpenFile).
const createRetries = 32

// testHookOpened, when set by a test, runs after each level of a private directory is opened,
// with that level's path, so a test can replace a path between two levels.
var testHookOpened func(path string)

// Dir is a private directory held open. Every operation through it is relative to the
// descriptor that was checked when the directory was opened, never to its path: replacing the
// directory, or an ancestor, with a symlink afterwards redirects nothing, as a path reopened
// for each operation would. Close it when done.
type Dir struct {
	root *os.Root
	path string
}

// Path is the directory's path as it was opened, for messages.
func (d *Dir) Path() string { return d.path }

// Root is the descriptor-relative view of the directory, for reads, renames, links and
// removals. Files are created through Create, Append, Write, WriteAtomic or OpenFile, which
// check what they open.
func (d *Dir) Root() *os.Root { return d.root }

// Close releases the directory's descriptor.
func (d *Dir) Close() error { return d.root.Close() }

// OpenPrivate ensures root and then each of elems below it (root/e1, root/e1/e2, …) is a
// private directory owned by the user with mode 0700, and returns the last one held open.
// Parents of root are created with 0700 if missing but are not otherwise checked, since system
// paths such as /tmp may legitimately be symlinks. root is opened without following a symlink,
// and each level below it relative to the descriptor of the level above, so no level is
// reached through a path that could have been replaced after the level above it was checked.
func OpenPrivate(root string, elems ...string) (*Dir, error) {
	d, _, err := open(root, elems, true)
	return d, err
}

// MkdirPrivate is OpenPrivate for a caller that needs only the check: it returns the full
// path, cleaned ("link/" or "link/." would make the kernel resolve the link before
// O_NOFOLLOW sees it).
func MkdirPrivate(root string, elems ...string) (string, error) {
	d, err := OpenPrivate(root, elems...)
	if err != nil {
		return "", err
	}
	_ = d.Close() // a read-only directory descriptor; closing it cannot lose data
	return d.path, nil
}

// OpenExisting opens root and each of elems below it the way OpenPrivate does, but creates
// and changes nothing: each level must be a directory owned by the user and not a symlink.
// A missing level ends the walk: exists is false and err nil, since there is nothing below it.
func OpenExisting(root string, elems ...string) (d *Dir, exists bool, err error) {
	return open(root, elems, false)
}

// CheckPrivate is OpenExisting for a caller that needs only the check.
func CheckPrivate(root string, elems ...string) (exists bool, err error) {
	d, exists, err := OpenExisting(root, elems...)
	if d != nil {
		_ = d.Close() // read-only; closing it cannot lose data
	}
	return exists, err
}

// Sub ensures name is a private directory in d, as OpenPrivate does for each level, and
// returns it held open.
func (d *Dir) Sub(name string) (*Dir, error) {
	r, err := openChild(d.root, name, filepath.Join(d.path, name), true)
	if err != nil {
		return nil, err
	}
	return &Dir{root: r, path: filepath.Join(d.path, name)}, nil
}

// Existing opens the directory name in d with OpenExisting's checks; exists is false when it
// is missing.
func (d *Dir) Existing(name string) (sub *Dir, exists bool, err error) {
	r, err := openChild(d.root, name, filepath.Join(d.path, name), false)
	if err != nil || r == nil {
		return nil, false, err
	}
	return &Dir{root: r, path: filepath.Join(d.path, name)}, true, nil
}

func open(root string, elems []string, create bool) (*Dir, bool, error) {
	path := filepath.Clean(root)
	for _, e := range elems {
		if err := component(e); err != nil {
			return nil, false, err
		}
	}
	if create {
		if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
			return nil, false, fmt.Errorf("fsx: create parent of %s: %w", path, err)
		}
	}
	r, err := openTop(path, create)
	if err != nil || r == nil {
		return nil, false, err
	}
	for _, e := range elems {
		if testHookOpened != nil {
			testHookOpened(path)
		}
		path = filepath.Join(path, e)
		child, err := openChild(r, e, path, create)
		_ = r.Close() // read-only; the child holds its own descriptor
		if err != nil || child == nil {
			return nil, false, err
		}
		r = child
	}
	if testHookOpened != nil {
		testHookOpened(path)
	}
	return &Dir{root: r, path: path}, true, nil
}

// component refuses a name that is not one directory entry: a separator or a dot name would
// make one level reach another.
func component(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) {
		return fmt.Errorf("fsx: %q is not a single path component", name)
	}
	return nil
}

// openTop opens path, the top of a private tree, without following a symlink, checks the
// descriptor, and returns the root on the same directory. nil and no error: path is missing
// and create is false.
func openTop(path string, create bool) (*os.Root, error) {
	if create {
		if err := syscall.Mkdir(path, dirMode); err != nil && !errors.Is(err, syscall.EEXIST) {
			return nil, fmt.Errorf("fsx: mkdir %s: %w", path, err)
		}
	}
	// O_NONBLOCK: a FIFO in its place must be refused, not waited on.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		switch {
		case !create && errors.Is(err, fs.ErrNotExist):
			return nil, nil
		case errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.ENOTDIR):
			return nil, fmt.Errorf("fsx: %s is a symlink or not a directory: %w", path, ErrUnsafePath)
		}
		return nil, fmt.Errorf("fsx: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }() // read-only; closing it cannot lose data
	if err := checkDir(f, path, create); err != nil {
		return nil, err
	}
	// os.Root has no constructor from a descriptor: open it by path, then prove it is the
	// directory just checked. A path replaced in between names another inode and is refused.
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("fsx: open %s: %w", path, err)
	}
	if err := sameDir(f, r, path); err != nil {
		_ = r.Close()
		return nil, err
	}
	return r, nil
}

// openChild opens name in parent, creating it first when create is set, and checks it. The
// parent is a checked private directory, so only the user can change its entries: the Lstat
// that refuses a symlink and the open that follows it see the same entry, and sameDir proves it.
func openChild(parent *os.Root, name, path string, create bool) (*os.Root, error) {
	if err := component(name); err != nil {
		return nil, err
	}
	if create {
		if err := parent.Mkdir(name, dirMode); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("fsx: mkdir %s: %w", path, err)
		}
	}
	fi, err := parent.Lstat(name)
	if err != nil {
		if !create && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("fsx: stat %s: %w", path, err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
		return nil, fmt.Errorf("fsx: %s is a symlink or not a directory: %w", path, ErrUnsafePath)
	}
	r, err := parent.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("fsx: open %s: %w", path, err)
	}
	f, err := r.Open(".")
	if err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("fsx: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }() // read-only; closing it cannot lose data
	if st, err := f.Stat(); err != nil || !os.SameFile(fi, st) {
		_ = r.Close()
		return nil, fmt.Errorf("fsx: %s changed while it was opened: %w", path, ErrUnsafePath)
	}
	if err := checkDir(f, path, create); err != nil {
		_ = r.Close()
		return nil, err
	}
	return r, nil
}

// sameDir checks that r is the directory f has open.
func sameDir(f *os.File, r *os.Root, path string) error {
	a, err := f.Stat()
	if err != nil {
		return fmt.Errorf("fsx: stat %s: %w", path, err)
	}
	b, err := r.Stat(".")
	if err != nil {
		return fmt.Errorf("fsx: stat %s: %w", path, err)
	}
	if !os.SameFile(a, b) {
		return fmt.Errorf("fsx: %s changed while it was opened: %w", path, ErrUnsafePath)
	}
	return nil
}

// checkDir checks owner and mode on the descriptor, so the check and the fchmod apply to the
// same inode. fix tightens (or repairs) the mode to exactly 0700: wider leaks, and narrower
// (0500, 0600) would leave recite unable to write or search the directory it was promised.
func checkDir(f *os.File, path string, fix bool) error {
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("fsx: stat %s: %w", path, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("fsx: %s is a symlink or not a directory: %w", path, ErrUnsafePath)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("fsx: %s is owned by uid %d, not by you: %w", path, st.Uid, ErrUnsafePath)
	}
	if fix && fi.Mode().Perm() != dirMode {
		if err := f.Chmod(dirMode); err != nil {
			return fmt.Errorf("fsx: set %s to 0700: %w", path, err)
		}
	}
	return nil
}

// OpenFile opens name in d with flag (os.O_WRONLY, os.O_APPEND, os.O_CREATE, …), creating it
// 0600, and checks the descriptor before anything is written: a regular file owned by the
// user. A symlink or a non-regular file at name is refused before the open, so a FIFO is never
// waited on and nothing is created through a link. d is a checked private directory, so only
// the user's own processes can change its entries between that check and the open.
func (d *Dir) OpenFile(name string, flag int) (*os.File, error) {
	path := filepath.Join(d.path, name)
	if err := component(name); err != nil {
		return nil, err
	}
	if fi, err := d.root.Lstat(name); err == nil && !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("fsx: %s is a symlink or not a regular file: %w", path, ErrUnsafePath)
	}
	f, err := d.root.OpenFile(name, flag|syscall.O_NONBLOCK|syscall.O_CLOEXEC, fileMode)
	// macOS: openat(2) with O_CREAT and without O_EXCL fails with ENOENT now and then while another
	// thread or process creates the same name; open(2) by path does not. Once the race is over the
	// name exists, so a retry opens it. A directory that is really gone keeps failing.
	for try := 0; try < createRetries && flag&(os.O_CREATE|os.O_EXCL) == os.O_CREATE && errors.Is(err, fs.ErrNotExist); try++ {
		f, err = d.root.OpenFile(name, flag|syscall.O_NONBLOCK|syscall.O_CLOEXEC, fileMode)
	}
	if err != nil {
		if errors.Is(err, syscall.ENXIO) {
			return nil, fmt.Errorf("fsx: %s is not a regular file: %w", path, ErrUnsafePath)
		}
		return nil, fmt.Errorf("fsx: open %s: %w", path, err)
	}
	if err := checkFile(f, path, false); err != nil {
		_ = f.Close() // already failing; err is the one to report
		return nil, err
	}
	return f, nil
}

// Create opens name in d for writing with mode 0600, truncating it, with CreatePrivate's
// checks.
func (d *Dir) Create(name string) (*os.File, error) {
	f, err := d.openPrivate(name, 0)
	if err != nil {
		return nil, err
	}
	return truncate(f, filepath.Join(d.path, name))
}

// Append opens name in d for appending with mode 0600, with AppendPrivate's checks.
func (d *Dir) Append(name string) (*os.File, error) { return d.openPrivate(name, os.O_APPEND) }

// Write writes data to name in d with mode 0600 (see Create).
func (d *Dir) Write(name string, data []byte) error {
	f, err := d.Create(name)
	if err != nil {
		return err
	}
	return writeClose(f, filepath.Join(d.path, name), data)
}

// WriteAtomic writes data to name in d through a 0600 temporary file in d and a rename, so a
// reader sees the old content or the new, never part of it. A failure removes the temporary
// file, best effort.
func (d *Dir) WriteAtomic(name string, data []byte) (err error) {
	if err := component(name); err != nil {
		return err
	}
	f, tmp, err := d.CreateTemp("." + name + ".tmp-")
	if err != nil {
		return fmt.Errorf("fsx: temp for %s: %w", name, err)
	}
	defer func() {
		if err != nil {
			_ = d.root.Remove(tmp) // best effort: a stray temp file is never read as the file
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsx: write %s: %w", name, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsx: sync %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("fsx: close %s: %w", name, err)
	}
	if err := d.root.Rename(tmp, name); err != nil {
		return fmt.Errorf("fsx: rename %s: %w", name, err)
	}
	return nil
}

// CreateTemp creates a new file in d, mode 0600, named prefix and a random suffix, and returns
// it with its name in d. O_EXCL never reuses an existing name and never follows a link.
func (d *Dir) CreateTemp(prefix string) (f *os.File, name string, err error) {
	for range 16 {
		var rnd [8]byte
		_, _ = rand.Read(rnd[:]) // crypto/rand.Read never returns an error
		name = prefix + hex.EncodeToString(rnd[:])
		if err := component(name); err != nil {
			return nil, "", err
		}
		f, err = d.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_CLOEXEC, fileMode)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("fsx: create in %s: %w", d.path, err)
		}
		if err := f.Chmod(fileMode); err != nil { // the umask may have narrowed it
			_ = f.Close()
			_ = d.root.Remove(name)
			return nil, "", fmt.Errorf("fsx: chmod %s: %w", filepath.Join(d.path, name), err)
		}
		return f, name, nil
	}
	return nil, "", fmt.Errorf("fsx: create in %s: %w", d.path, fs.ErrExist)
}

func (d *Dir) openPrivate(name string, flags int) (*os.File, error) {
	f, err := d.OpenFile(name, os.O_WRONLY|os.O_CREATE|flags)
	if err != nil {
		return nil, err
	}
	if err := privateFile(f, filepath.Join(d.path, name)); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// CreatePrivate opens path for writing with mode 0600, truncating it. It refuses to follow a
// symlink at path, and checks the opened descriptor before changing anything: it must be a
// regular file owned by the user with one link, so a FIFO cannot block the open (O_NONBLOCK)
// and neither a hard link nor another user's file is truncated or chmodded. An existing wider
// file is tightened through fchmod. For a file in a private directory, use Dir.Create.
func CreatePrivate(path string) (*os.File, error) {
	f, err := openPrivate(path, 0)
	if err != nil {
		return nil, err
	}
	return truncate(f, path)
}

// AppendPrivate opens path for appending with mode 0600, with CreatePrivate's checks: an
// existing file is kept, and tightened to 0600 if it was wider.
func AppendPrivate(path string) (*os.File, error) { return openPrivate(path, os.O_APPEND) }

// openPrivate opens path write-only, creating it 0600, and checks the descriptor before
// changing anything (CreatePrivate).
func openPrivate(path string, flags int) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC|flags, fileMode)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("fsx: %s is a symlink: %w", path, ErrUnsafePath)
		}
		if errors.Is(err, syscall.ENXIO) {
			return nil, fmt.Errorf("fsx: %s is not a regular file: %w", path, ErrUnsafePath)
		}
		return nil, fmt.Errorf("fsx: create %s: %w", path, err)
	}
	if err := checkFile(f, path, true); err != nil {
		_ = f.Close() // already failing; err is the one to report
		return nil, err
	}
	if err := f.Chmod(fileMode); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("fsx: chmod %s: %w", path, err)
	}
	return f, nil
}

// privateFile adds CreatePrivate's single-link rule and mode to a file Dir.OpenFile checked.
func privateFile(f *os.File, path string) error {
	if err := checkFile(f, path, true); err != nil {
		return err
	}
	if err := f.Chmod(fileMode); err != nil {
		return fmt.Errorf("fsx: chmod %s: %w", path, err)
	}
	return nil
}

// checkFile checks the descriptor: a regular file owned by the user, and with oneLink a file
// with no other name, so a hard link to someone else's data is never truncated or chmodded.
func checkFile(f *os.File, path string, oneLink bool) error {
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("fsx: stat %s: %w", path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.Mode().IsRegular():
		return fmt.Errorf("fsx: %s is not a regular file: %w", path, ErrUnsafePath)
	case ok && int(st.Uid) != os.Getuid():
		return fmt.Errorf("fsx: %s is owned by uid %d, not by you: %w", path, st.Uid, ErrUnsafePath)
	case oneLink && ok && st.Nlink != 1:
		return fmt.Errorf("fsx: %s has %d links: %w", path, st.Nlink, ErrUnsafePath)
	}
	return nil
}

func truncate(f *os.File, path string) (*os.File, error) {
	if err := f.Truncate(0); err != nil {
		_ = f.Close() // already failing; the truncate error is the one to report
		return nil, fmt.Errorf("fsx: truncate %s: %w", path, err)
	}
	return f, nil
}

func writeClose(f *os.File, path string, data []byte) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsx: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("fsx: close %s: %w", path, err)
	}
	return nil
}

// WritePrivate writes data to path with mode 0600 (see CreatePrivate).
func WritePrivate(path string, data []byte) error {
	f, err := CreatePrivate(path)
	if err != nil {
		return err
	}
	return writeClose(f, path, data)
}
