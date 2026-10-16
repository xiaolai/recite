package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/xiaolai/recite/internal/fsx"
)

// FileName is the ledger in the state directory (design §11.3).
const FileName = "usage.jsonl"

// Kind is what a ledger line accounts for.
type Kind string

// Ledger line kinds: one line per render and per STT call.
const (
	KindRender Kind = "render"
	KindSTT    Kind = "stt"
)

// Entry is one ledger line. It names models and chunk ids only: never a token, an email or
// an account identifier.
type Entry struct {
	Time    time.Time `json:"time"`
	Command string    `json:"command"`
	Kind    Kind      `json:"kind"`
	Model   string    `json:"model"`
	Chunk   string    `json:"chunk"`
	Attempt int       `json:"attempt"`

	InputTokens       int64 `json:"input_tokens,omitempty"`
	OutputTokens      int64 `json:"output_tokens,omitempty"`
	InputTextTokens   int64 `json:"input_text_tokens,omitempty"`
	OutputTextTokens  int64 `json:"output_text_tokens,omitempty"`
	OutputAudioTokens int64 `json:"output_audio_tokens,omitempty"`
	STTSeconds        int   `json:"stt_seconds,omitempty"`

	// EstimateUSD is the list-price estimate; null when the price table has no rate for the
	// model. PriceNote labels it (Note) and names the table's dated source.
	EstimateUSD *float64 `json:"estimate_usd"`
	PriceNote   string   `json:"price_note"`
}

// Ledger appends entries to <Dir>/usage.jsonl. Any number of goroutines and processes may
// append at once: each line is one write(2) on an O_APPEND descriptor, which the kernel
// positions and writes as a unit for a regular file, so lines never interleave (OBS-03).
type Ledger struct {
	dir string
	now func() time.Time
}

// Open prepares the private ledger directory (0700). now nil means time.Now.
func Open(dir string, now func() time.Time) (*Ledger, error) {
	if dir == "" {
		return nil, errors.New("usage: no ledger directory")
	}
	if _, err := fsx.MkdirPrivate(dir); err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &Ledger{dir: dir, now: now}, nil
}

// Path is the current ledger file.
func (l *Ledger) Path() string { return filepath.Join(l.dir, FileName) }

// Append writes e as one line, after rotating a ledger last written in an earlier month.
func (l *Ledger) Append(e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("usage: encode entry: %w", err)
	}
	b = append(b, '\n')
	if err := l.rotate(); err != nil {
		return err
	}
	f, err := os.OpenFile(l.Path(), os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return fmt.Errorf("usage: %s is a symlink: %w", l.Path(), fsx.ErrUnsafePath)
		}
		return fmt.Errorf("usage: open ledger: %w", err)
	}
	n, werr := f.Write(b) // one write: the line must never be split
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil && n != len(b) {
		werr = fmt.Errorf("short write %d of %d bytes", n, len(b))
	}
	if werr != nil {
		return fmt.Errorf("usage: append to ledger: %w", werr)
	}
	return nil
}

// ArchiveName is the name a ledger last written in the given month is rotated to.
func ArchiveName(t time.Time) string {
	t = t.UTC()
	return fmt.Sprintf("usage-%04d-%02d.jsonl", t.Year(), int(t.Month()))
}

// rotate moves a ledger last modified before the current month (UTC) to its month's archive
// (design §11.3: rotated monthly). link(2) refuses an existing archive, so of several
// processes rotating at once exactly one wins and removes the old name; the others append to
// whichever file the name then holds. No line is lost.
func (l *Ledger) rotate() error {
	fi, err := os.Lstat(l.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("usage: stat ledger: %w", err)
	}
	last, now := fi.ModTime().UTC(), l.now().UTC()
	// Only a month strictly before the current one rotates: a clock set back must not archive
	// the current ledger.
	if last.Year()*12+int(last.Month()) >= now.Year()*12+int(now.Month()) {
		return nil
	}
	archive := filepath.Join(l.dir, ArchiveName(last))
	if err := os.Link(l.Path(), archive); err != nil {
		if errors.Is(err, fs.ErrExist) || errors.Is(err, fs.ErrNotExist) {
			return nil // another process rotated it
		}
		return fmt.Errorf("usage: rotate ledger: %w", err)
	}
	if err := os.Remove(l.Path()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("usage: rotate ledger: %w", err)
	}
	return nil
}
