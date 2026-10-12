package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// usageError is a failure of the user's input or environment: exit 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return &usageError{fmt.Sprintf(format, a...)} }

// defaultOutput derives the output path (OUT-01): the input file's stem with
// ext in the current directory, or speech-YYYYMMDD-HHMMSS<ext> for text and stdin.
func defaultOutput(file string, now time.Time, ext string) string {
	if file != "" && file != "-" {
		base := filepath.Base(file)
		if stem := strings.TrimSuffix(base, filepath.Ext(base)); stem != "" && stem != "." {
			return stem + ext
		}
	}
	return "speech-" + now.Format("20060102-150405") + ext
}

// preflight fails fast, before any network call, when the target exists and
// --force is absent (OUT-01), the target is a directory, or its directory is missing. Lstat, so a dangling symlink counts as existing.
func preflight(target string, force bool) error {
	dir := filepath.Dir(target)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return usagef("output directory %s does not exist", dir)
	}
	fi, err := os.Lstat(target)
	switch {
	case err == nil && fi.IsDir():
		// rename(2) cannot replace a directory, so --force would only fail after the renders.
		return usagef("%s is a directory; name an output file", target)
	case err == nil && force:
		return nil
	case err == nil:
		return usagef("%s already exists; pass --force to overwrite it", target)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	}
	return fmt.Errorf("check output %s: %w", target, err)
}

// errTargetAppeared marks a target created after the pre-flight check.
var errTargetAppeared = errors.New("output appeared after the pre-flight check")

// publishOutput writes the output through a temp file in the target directory
// and publishes it atomically (OUT-01): a reader of target sees either nothing
// (or its previous content) or the complete file, even after SIGKILL. Without
// force, link(2) refuses an existing target; the audio is then kept at the
// reported temp path. With force, rename(2) replaces the target, including a
// symlink, which is replaced rather than followed. write gets the open temp file; an
// external encoder may write to it by name instead.
func publishOutput(target string, force bool, write func(*os.File) error) error {
	f, tmp, err := createTemp(target)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmp) // best effort: the temp name is ours alone
		}
	}()
	err = write(f)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write output %s: %w", tmp, err)
	}
	if force {
		if err := os.Rename(tmp, target); err != nil {
			return fmt.Errorf("publish output: %w", err)
		}
		return syncDir(filepath.Dir(target))
	}
	if err := os.Link(tmp, target); err != nil {
		if errors.Is(err, fs.ErrExist) {
			keep = true
			return &usageError{fmt.Sprintf("%s: %s already exists (created during the run); the audio is kept at %s",
				errTargetAppeared, target, tmp)}
		}
		return fmt.Errorf("publish output: %w", err)
	}
	if err := os.Remove(tmp); err != nil {
		return fmt.Errorf("remove temp output: %w", err)
	}
	return syncDir(filepath.Dir(target))
}

// createTemp creates a fresh file next to target with O_EXCL, open for reading and writing
// (the assembly is written to one and read back to publish it). Its mode follows
// the umask, as outputs are not private files (design §14.2).
func createTemp(target string) (*os.File, string, error) {
	dir, base := filepath.Split(target)
	for range 10 {
		var r [6]byte
		if _, err := rand.Read(r[:]); err != nil {
			return nil, "", fmt.Errorf("temp name: %w", err)
		}
		tmp := filepath.Join(dir, "."+base+"."+hex.EncodeToString(r[:])+".tmp")
		f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
		if err == nil {
			return f, tmp, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", fmt.Errorf("create temp output: %w", err)
		}
	}
	return nil, "", fmt.Errorf("create temp output in %q: too many name collisions", dir)
}

// syncDir makes the published name durable.
func syncDir(dir string) error {
	if dir == "" {
		dir = "."
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("sync output directory: %w", err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync output directory: %w", err)
	}
	return nil
}

// distinctOutputs fails when two of a run's output paths name the same file: the later
// publication would replace the earlier one. Paths are compared cleaned and absolute, and, for
// files that exist, by identity, so a symlink or a hard link to another output also collides.
// Empty paths (an output that is off) are skipped.
func distinctOutputs(paths ...string) error {
	type seen struct {
		path string
		fi   os.FileInfo
	}
	var prev []seen
	for _, p := range paths {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return fmt.Errorf("resolve output %s: %w", p, err)
		}
		fi, _ := os.Stat(abs) // a missing file can only collide by name
		for _, q := range prev {
			if q.path == abs || (fi != nil && q.fi != nil && os.SameFile(fi, q.fi)) {
				return usagef("%s and %s are the same file; give each output its own path", q.path, abs)
			}
		}
		prev = append(prev, seen{abs, fi})
	}
	return nil
}

// assemblySpool is the temp file `say` assembles the PCM into: beside target (in its parent
// directory under --split-sections), on the output's file system. The caller closes and
// removes it.
func assemblySpool(target string) (*os.File, error) {
	f, _, err := createTemp(filepath.Clean(target) + ".pcm")
	if err != nil {
		return nil, fmt.Errorf("assembly: %w", err)
	}
	return f, nil
}
