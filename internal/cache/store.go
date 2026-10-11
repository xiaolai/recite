package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/xiaolai/recite/internal/fsx"
)

// EntrySchema identifies the entry JSON format.
const EntrySchema = "recite/take@1"

// Final verdicts of a cached take.
const (
	VerdictAccepted   = "accepted"
	VerdictUnverified = "unverified"
)

// Guard verdicts. A guard that did not judge the take (the language guard of an uncertain
// language, a post-STT guard under --verify off) is recorded as skipped, never as pass.
const (
	GuardPass    = "pass"
	GuardFail    = "fail"
	GuardSkipped = "skipped"
)

// GuardVerdict is one guard's verdict and the hash of the inputs it judged.
type GuardVerdict struct {
	Verdict     string `json:"verdict"`
	ContextHash string `json:"context_hash"`
}

// Verification is the provenance of a cached take's verdict (design §11.1, R#25): what it was
// judged by and under which policy, so a later run can re-judge whether it still holds.
type Verification struct {
	Verdict         string                  `json:"verdict"`
	Guards          map[string]GuardVerdict `json:"guards"`
	Score           float64                 `json:"score"`
	Route           string                  `json:"route"`
	STTModel        string                  `json:"stt_model"`
	CanonVersion    int                     `json:"canon_version"`
	GuardVersion    int                     `json:"guard_version"`
	Mode            string                  `json:"mode"`
	AcceptThreshold float64                 `json:"accept_threshold"`
}

// Entry is the JSON record of one cached take. The raw take (untrimmed audio, said text,
// response status) is kept so every pre-STT guard can re-run on it.
type Entry struct {
	Schema       string       `json:"schema"`
	Key          string       `json:"key"`
	ChunkID      string       `json:"chunk_id"`
	Said         string       `json:"said"`
	Status       string       `json:"status"`
	Transcript   string       `json:"transcript"`
	Score        float64      `json:"score"`
	Attempts     int          `json:"attempts"`
	PCMBytes     int          `json:"pcm_bytes"`
	PCMSHA256    string       `json:"pcm_sha256"`
	CreatedAt    time.Time    `json:"created_at"`
	Verification Verification `json:"verification"`
}

// Store is the take store under <root>/takes, private (0700 directories, 0600 files).
// Writes are atomic (temporary file and rename), so concurrent runs never see a partial
// entry; an entry that fails to parse or whose audio does not match its recorded hash is a
// miss, never trusted.
type Store struct {
	dir string
	now func() time.Time
}

// Open opens (creating) the store under root. now stamps entries and access times; nil
// means time.Now.
func Open(root string, now func() time.Time) (*Store, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("cache: %q: want an absolute directory", root)
	}
	dir, err := fsx.MkdirPrivate(root, "takes")
	if err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	if now == nil {
		now = time.Now
	}
	return &Store{dir: dir, now: now}, nil
}

// Dir is the takes directory.
func (s *Store) Dir() string { return s.dir }

func validKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil
}

func (s *Store) base(key string) string { return filepath.Join(s.dir, key[:2], key) }

// Get returns the entry and raw PCM of key. ok is false on a miss, including an entry that is
// partial, corrupt, or recorded under another key; err reports only why a present entry was
// rejected, for the log. A hit records the access for pruning.
func (s *Store) Get(key string) (e Entry, pcm []byte, ok bool, err error) {
	e, pcm, ok, err = s.load(key)
	if ok {
		s.touch(key)
	}
	return e, pcm, ok, err
}

// Peek is Get without recording the access: a plan that only counts hits (--dry-run) must not
// change what pruning keeps.
func (s *Store) Peek(key string) (ok bool, err error) {
	_, _, ok, err = s.load(key)
	return ok, err
}

// load reads and validates the entry and audio of key.
func (s *Store) load(key string) (e Entry, pcm []byte, ok bool, err error) {
	if !validKey(key) {
		return Entry{}, nil, false, fmt.Errorf("cache: invalid key %q", key)
	}
	b := s.base(key)
	raw, err := os.ReadFile(b + ".json")
	if errors.Is(err, fs.ErrNotExist) {
		return Entry{}, nil, false, nil
	}
	if err != nil {
		return Entry{}, nil, false, fmt.Errorf("cache: read %s: %w", key, err)
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return Entry{}, nil, false, fmt.Errorf("cache: entry %s: %w", key, err)
	}
	if e.Schema != EntrySchema || e.Key != key {
		return Entry{}, nil, false, fmt.Errorf("cache: entry %s: schema %q, key %q", key, e.Schema, e.Key)
	}
	pcm, err = os.ReadFile(b + ".pcm")
	if err != nil {
		return Entry{}, nil, false, fmt.Errorf("cache: audio %s: %w", key, err)
	}
	sum := sha256.Sum256(pcm)
	if len(pcm) != e.PCMBytes || hex.EncodeToString(sum[:]) != e.PCMSHA256 {
		return Entry{}, nil, false, fmt.Errorf("cache: audio %s does not match its entry", key)
	}
	return e, pcm, true, nil
}

// touch records the access time in the index (one small file per entry; atime is not
// trusted, design §11.1). A failure only loses pruning precision, so it is ignored.
func (s *Store) touch(key string) {
	_ = writeAtomic(filepath.Join(s.dir, key[:2]), key+".access", []byte(strconv.FormatInt(s.now().UnixNano(), 10)))
}

// Accessed returns the last access time recorded for key.
func (s *Store) Accessed(key string) (time.Time, bool) {
	if !validKey(key) {
		return time.Time{}, false
	}
	b, err := os.ReadFile(s.base(key) + ".access")
	if err != nil {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, n), true
}

// Put stores the raw take of e.Key. The audio is written first and the entry last, so an
// interrupted Put leaves at most audio without an entry: a miss.
func (s *Store) Put(e Entry, pcm []byte) error {
	if !validKey(e.Key) {
		return fmt.Errorf("cache: invalid key %q", e.Key)
	}
	e.Schema = EntrySchema
	e.PCMBytes = len(pcm)
	sum := sha256.Sum256(pcm)
	e.PCMSHA256 = hex.EncodeToString(sum[:])
	if e.CreatedAt.IsZero() {
		e.CreatedAt = s.now().UTC()
	}
	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("cache: encode %s: %w", e.Key, err)
	}
	sub, err := fsx.MkdirPrivate(s.dir, e.Key[:2])
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	if err := writeAtomic(sub, e.Key+".pcm", pcm); err != nil {
		return err
	}
	if err := writeAtomic(sub, e.Key+".json", append(raw, '\n')); err != nil {
		return err
	}
	s.touch(e.Key)
	return nil
}

// writeAtomic writes name in dir through a 0600 temporary file and a rename.
func writeAtomic(dir, name string, data []byte) (err error) {
	f, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("cache: temp for %s: %w", name, err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp) // best effort: a stray temp file is never read as an entry
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("cache: chmod %s: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("cache: write %s: %w", name, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("cache: sync %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("cache: close %s: %w", name, err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("cache: rename %s: %w", name, err)
	}
	return nil
}
