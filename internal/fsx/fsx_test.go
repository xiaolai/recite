package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// accept: AUTH-05#5
func TestPrivateModes(t *testing.T) {
	old := syscall.Umask(0) // a permissive umask must not widen private modes
	defer syscall.Umask(old)
	root := filepath.Join(t.TempDir(), "state")
	p, err := MkdirPrivate(root, "debug", "rejected")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{root, filepath.Join(root, "debug"), p} {
		if m := mode(t, d); m != 0o700 {
			t.Errorf("%s mode %o", d, m)
		}
	}
	f := filepath.Join(p, "take.wav")
	if err := os.WriteFile(f, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(f, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, f); m != 0o600 {
		t.Errorf("file mode %o", m)
	}
	n := filepath.Join(p, "new")
	if err := WritePrivate(n, []byte("y")); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, n); m != 0o600 {
		t.Errorf("new file mode %o", m)
	}
}

// accept: AUTH-05#5
func TestExistingPermissiveDirTightened(t *testing.T) {
	d := filepath.Join(t.TempDir(), "debug")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := MkdirPrivate(d); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, d); m != 0o700 {
		t.Fatalf("mode %o, want 700", m)
	}
}

// accept: AUTH-05#7
func TestSymlinkRefusedDestinationUntouched(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "debug")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, try := range []func() error{
		func() error { _, err := MkdirPrivate(link); return err },
		func() error { _, err := MkdirPrivate(root, "debug", "sub"); return err },
	} {
		if err := try(); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("err = %v, want ErrUnsafePath", err)
		}
	}
	fileLink := filepath.Join(root, "file")
	victim := filepath.Join(target, "victim")
	if err := os.WriteFile(victim, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, fileLink); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(fileLink, []byte("evil")); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("file symlink err = %v", err)
	}
	if m := mode(t, target); m != 0o755 {
		t.Errorf("destination chmodded to %o", m)
	}
	ents, err := os.ReadDir(target)
	if err != nil || len(ents) != 1 {
		t.Fatalf("destination written: %v %v", ents, err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "orig" {
		t.Errorf("victim overwritten: %q", b)
	}
	if m := mode(t, victim); m != 0o644 {
		t.Errorf("victim chmodded to %o", m)
	}
}

func TestNonDirectoryRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MkdirPrivate(p); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("err = %v", err)
	}
}

// A root written with a trailing separator is still refused when it is a symlink: the kernel
// resolves "link/" before O_NOFOLLOW sees it (audit).
func TestTrailingSeparatorSymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{link + "/", link + "/."} {
		if _, err := MkdirPrivate(root, "takes"); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("MkdirPrivate(%q) = %v, want ErrUnsafePath", root, err)
		}
		if _, err := CheckPrivate(root); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("CheckPrivate(%q) = %v, want ErrUnsafePath", root, err)
		}
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("the link's target changed: %v %v", fi.Mode(), err)
	}
	if _, err := os.Stat(filepath.Join(target, "takes")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a directory was created through the link")
	}
}

// An existing directory narrower than 0700 is repaired, so recite can write in it.
func TestExistingNarrowDirRepaired(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v, want 0700", fi.Mode().Perm())
	}
}

// A hard link is refused before anything is truncated or chmodded.
func TestCreatePrivateRefusesHardLink(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "take.wav")
	if err := os.Link(other, path); err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePrivate(path); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("CreatePrivate on a hard link = %v, want ErrUnsafePath", err)
	}
	if b, _ := os.ReadFile(other); string(b) != "keep" {
		t.Fatalf("the linked file now holds %q", b)
	}
	if fi, _ := os.Stat(other); fi.Mode().Perm() != 0o644 {
		t.Fatalf("the linked file's mode changed to %v", fi.Mode().Perm())
	}
}

// A FIFO is refused at once, never waited on.
func TestCreatePrivateRefusesFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "take.wav")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := CreatePrivate(path)
		if f != nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("CreatePrivate on a FIFO = %v, want ErrUnsafePath", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CreatePrivate blocked on a FIFO")
	}
}

// An existing private file is truncated and kept at 0600.
func TestCreatePrivateTruncatesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "take.json")
	if err := os.WriteFile(path, []byte("old contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Fatalf("file holds %q", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

// A directory held open stays the directory that was checked: once its path is replaced by a
// symlink, every write through it still lands in the original directory, and the link's
// destination is untouched (audit: each operation reopened the full path).
// accept: AUTH-05#8
func TestDirIsAnchoredToItsDescriptor(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "state")
	d, err := OpenPrivate(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	evil := filepath.Join(base, "evil")
	if err := os.Mkdir(evil, 0o755); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(base, "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(evil, root); err != nil {
		t.Fatal(err)
	}
	if err := d.Write("a", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteAtomic("b", []byte("2")); err != nil {
		t.Fatal(err)
	}
	sub, err := d.Sub("takes")
	if err != nil {
		t.Fatal(err)
	}
	if err := sub.Write("c", []byte("3")); err != nil {
		t.Fatal(err)
	}
	_ = sub.Close()
	f, err := d.Append("log")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	for _, p := range []string{"a", "b", filepath.Join("takes", "c"), "log"} {
		if _, err := os.Stat(filepath.Join(moved, p)); err != nil {
			t.Errorf("%s not in the original directory: %v", p, err)
		}
	}
	if ents, err := os.ReadDir(evil); err != nil || len(ents) != 0 {
		t.Fatalf("the symlink's destination was written: %v %v", ents, err)
	}
	if m := mode(t, evil); m != 0o755 {
		t.Errorf("the symlink's destination was chmodded to %o", m)
	}
}

// Each level is opened relative to the level above it: replacing the top with a symlink after
// it was checked does not lead the walk into the link's destination (audit).
// accept: AUTH-05#8
func TestLevelsAreOpenedRelativeToTheLevelAbove(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "state")
	evil := filepath.Join(base, "evil")
	if err := os.MkdirAll(filepath.Join(evil, "debug"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(evil, "debug"), 0o755); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(base, "moved")
	swapped := false
	testHookOpened = func(p string) {
		if p == root && !swapped {
			swapped = true
			if err := os.Rename(root, moved); err != nil {
				t.Error(err)
			}
			if err := os.Symlink(evil, root); err != nil {
				t.Error(err)
			}
		}
	}
	defer func() { testHookOpened = nil }()
	if _, err := MkdirPrivate(root, "debug", "rejected"); err != nil {
		t.Fatal(err)
	}
	if !swapped {
		t.Fatal("the hook never ran")
	}
	if _, err := os.Stat(filepath.Join(moved, "debug", "rejected")); err != nil {
		t.Fatalf("the levels were not created in the checked directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(evil, "debug", "rejected")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a level was created through the replaced path")
	}
	if m := mode(t, filepath.Join(evil, "debug")); m != 0o755 {
		t.Errorf("the destination's directory was chmodded to %o", m)
	}
}

// OpenExisting creates nothing, reports a missing level, and refuses what CheckPrivate refuses.
func TestOpenExisting(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	if d, ok, err := OpenExisting(root, "takes"); d != nil || ok || err != nil {
		t.Fatalf("missing root: %v %v %v", d, ok, err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("OpenExisting created the root")
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if d, ok, err := OpenExisting(root, "takes"); d != nil || ok || err != nil {
		t.Fatalf("missing level: %v %v %v", d, ok, err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "takes")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenExisting(root, "takes"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlinked level: %v", err)
	}
	if m := mode(t, root); m != 0o755 {
		t.Errorf("OpenExisting changed the mode to %o", m)
	}
}

// A level name must be one directory entry.
func TestLevelNameIsOneComponent(t *testing.T) {
	root := t.TempDir()
	for _, e := range []string{"", ".", "..", "a/b"} {
		if _, err := MkdirPrivate(root, e); err == nil {
			t.Errorf("MkdirPrivate(%q) accepted", e)
		}
	}
	d, err := OpenPrivate(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	for _, e := range []string{"..", "a/b"} {
		if err := d.Write(e, nil); err == nil {
			t.Errorf("Write(%q) accepted", e)
		}
		if _, err := d.Sub(e); err == nil {
			t.Errorf("Sub(%q) accepted", e)
		}
	}
}

// Files opened through a Dir have CreatePrivate's checks: a symlink, a FIFO and a hard link
// are refused, the FIFO at once.
func TestDirFileChecks(t *testing.T) {
	dir := t.TempDir()
	d, err := OpenPrivate(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("victim", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, filepath.Join(dir, "hard")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, name := range []string{"link", "hard", "fifo"} {
			if err := d.Write(name, []byte("evil")); !errors.Is(err, ErrUnsafePath) {
				t.Errorf("Write(%s) = %v, want ErrUnsafePath", name, err)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a write blocked on the FIFO")
	}
	if b, _ := os.ReadFile(victim); string(b) != "orig" {
		t.Errorf("victim overwritten: %q", b)
	}
	if m := mode(t, victim); m != 0o644 {
		t.Errorf("victim chmodded to %o", m)
	}
}

// Many goroutines creating the same file through one Dir all succeed: on macOS, openat(2) with
// O_CREAT fails with ENOENT now and then while another caller creates the name (the usage
// ledger's concurrent appends hit it).
func TestConcurrentCreatesOfOneName(t *testing.T) {
	for range 20 {
		d, err := OpenPrivate(filepath.Join(t.TempDir(), "state"))
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 64*4)
		for range 64 {
			wg.Go(func() {
				for range 4 {
					f, err := d.OpenFile("usage.jsonl", os.O_WRONLY|os.O_APPEND|os.O_CREATE)
					if err != nil {
						errs <- err
						continue
					}
					_ = f.Close()
				}
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		_ = d.Close()
	}
}
