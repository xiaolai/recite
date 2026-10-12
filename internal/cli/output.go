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
	s, err := stageOutput(target, write)
	if err != nil {
		return err
	}
	_, err = s.publish(force)
	return err
}

// stagedOutput is an output written in full to a temp file beside its target, not yet
// published. A run stages every file before it publishes any, so a write or encoder failure
// publishes nothing.
type stagedOutput struct {
	target, tmp string
	done        bool // published, or kept at tmp for the user: the temp file is not ours to remove
}

// stageOutput writes the output to a temp file in the target directory. write gets the open
// temp file; an external encoder may write to it by name instead.
func stageOutput(target string, write func(*os.File) error) (*stagedOutput, error) {
	f, tmp, err := createTemp(target)
	if err != nil {
		return nil, err
	}
	err = write(f)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp) // best effort: the temp name is ours alone
		return nil, fmt.Errorf("write output %s: %w", tmp, err)
	}
	return &stagedOutput{target: target, tmp: tmp}, nil
}

// discard removes the temp file of an output that was not published.
func (s *stagedOutput) discard() {
	if !s.done {
		_ = os.Remove(s.tmp) // best effort: the temp name is ours alone
	}
}

// publish moves the staged file to its target. published reports whether target now holds
// it, which stays true when a later step (removing the link's temp name, syncing the
// directory) fails.
func (s *stagedOutput) publish(force bool) (published bool, err error) {
	defer s.discard()
	if force {
		if err := os.Rename(s.tmp, s.target); err != nil {
			return false, fmt.Errorf("publish output: %w", err)
		}
		s.done = true
		return true, syncDir(filepath.Dir(s.target))
	}
	if err := os.Link(s.tmp, s.target); err != nil {
		if errors.Is(err, fs.ErrExist) {
			s.done = true
			return false, &usageError{fmt.Sprintf("%s: %s already exists (created during the run); the audio is kept at %s",
				errTargetAppeared, s.target, s.tmp)}
		}
		return false, fmt.Errorf("publish output: %w", err)
	}
	if err := os.Remove(s.tmp); err != nil {
		s.done = true
		return true, fmt.Errorf("remove temp output: %w", err)
	}
	s.done = true
	return true, syncDir(filepath.Dir(s.target))
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

// fileID is a file's identity on its file system, shared by all its names.
type fileID struct{ dev, ino uint64 }

// sameFile is os.SameFile; a variable so a test can prove the identity map makes it unused.
var sameFile = os.SameFile

// distinctOutputs fails when two of a run's output paths name the same file: the later
// publication would replace the earlier one. Paths are compared canonical (see canonicalPath) and, for
// files that exist, by identity, so a symlink or a hard link to another output also collides.
// Each path costs map lookups only: existing files are keyed by device and inode. Where the
// platform exposes no identity, os.SameFile runs among files sharing size and modification
// time, which every name of one file shares. Empty paths (an output that is off) are skipped.
func distinctOutputs(paths ...string) error {
	type existing struct {
		path string
		fi   os.FileInfo
	}
	type stamp struct {
		size int64
		mod  int64
	}
	byPath := make(map[string]bool, len(paths))
	byID := make(map[fileID]string)
	byStamp := make(map[stamp][]existing)
	for _, p := range paths {
		if p == "" {
			continue
		}
		canon, err := canonicalPath(p)
		if err != nil {
			return fmt.Errorf("resolve output %s: %w", p, err)
		}
		if byPath[canon] {
			return usagef("%s is named twice; give each output its own path (both name the same file)", canon)
		}
		byPath[canon] = true
		fi, err := os.Stat(canon)
		if err != nil {
			continue // a missing file can only collide by name
		}
		if id, ok := fileIdentity(fi); ok {
			if q, dup := byID[id]; dup {
				return usagef("%s and %s are the same file; give each output its own path", q, canon)
			}
			byID[id] = canon
			continue
		}
		k := stamp{fi.Size(), fi.ModTime().UnixNano()}
		for _, q := range byStamp[k] {
			if sameFile(fi, q.fi) {
				return usagef("%s and %s are the same file; give each output its own path", q.path, canon)
			}
		}
		byStamp[k] = append(byStamp[k], existing{canon, fi})
	}
	return nil
}

// canonicalPath is p made absolute and clean, with its longest existing ancestor resolved
// through symlinks and the missing rest joined back on, so alias/out.wav and real/out.wav
// compare equal before either exists. The final element is not resolved: publication replaces
// a symlink there rather than following it, and an existing one is compared by identity.
func canonicalPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	dir, rest := filepath.Dir(abs), filepath.Base(abs)
	for {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			return filepath.Join(resolved, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, nil
		}
		dir, rest = parent, filepath.Join(filepath.Base(dir), rest)
	}
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
