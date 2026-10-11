package usage

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var archiveRE = regexp.MustCompile(`^usage-\d{4}-\d{2}\.jsonl$`)

// ReadResult is every ledger line read from a state directory.
type ReadResult struct {
	Entries []Entry
	Bad     int // lines that did not parse; reported, never guessed at
}

// Read reads the current ledger and its monthly archives under dir. A missing directory or
// ledger is an empty result.
func Read(dir string) (ReadResult, error) {
	var res ReadResult
	names, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("usage: read %s: %w", dir, err)
	}
	for _, n := range names {
		if !n.Type().IsRegular() || (n.Name() != FileName && !archiveRE.MatchString(n.Name())) {
			continue
		}
		if err := readFile(filepath.Join(dir, n.Name()), &res); err != nil {
			return res, err
		}
	}
	return res, nil
}

func readFile(path string, res *ReadResult) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("usage: open %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var e Entry
			if json.Unmarshal(line, &e) != nil || e.Time.IsZero() || (e.Kind != KindRender && e.Kind != KindSTT) {
				res.Bad++
			} else {
				res.Entries = append(res.Entries, e)
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("usage: read %s: %w", filepath.Base(path), err)
		}
	}
}

// Row is the usage of one model on one day (UTC).
type Row struct {
	Day          string  `json:"day"`
	Model        string  `json:"model"`
	Renders      int     `json:"renders"`
	AudioSeconds float64 `json:"audio_seconds"`
	STTCalls     int     `json:"stt_calls"`
	STTSeconds   int     `json:"stt_seconds"`
	EstimateUSD  float64 `json:"estimate_usd"`
	Unpriced     int     `json:"unpriced"` // lines without a list-price rate, left out of EstimateUSD
}

// Summarize groups entries at or after since (zero: all) by day and model, sorted by day
// then model. Audio seconds are derived from output audio tokens at AudioTokensPerSecond:
// the ledger records tokens, not durations.
func Summarize(entries []Entry, since time.Time) []Row {
	rows := map[[2]string]*Row{}
	for _, e := range entries {
		if !since.IsZero() && e.Time.Before(since) {
			continue
		}
		day := e.Time.UTC().Format(time.DateOnly)
		k := [2]string{day, e.Model}
		r := rows[k]
		if r == nil {
			r = &Row{Day: day, Model: e.Model}
			rows[k] = r
		}
		switch e.Kind {
		case KindRender:
			r.Renders++
			audio := e.OutputAudioTokens
			if audio == 0 && e.OutputTextTokens == 0 {
				audio = e.OutputTokens // no output details: as Render prices it
			}
			r.AudioSeconds += float64(audio) / AudioTokensPerSecond
		case KindSTT:
			r.STTCalls++
			r.STTSeconds += e.STTSeconds
		}
		if e.EstimateUSD == nil {
			r.Unpriced++
		} else {
			r.EstimateUSD += *e.EstimateUSD
		}
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b Row) int { return cmp.Or(strings.Compare(a.Day, b.Day), strings.Compare(a.Model, b.Model)) })
	return out
}

// FormatSummary renders rows as the text table of `recite usage`, with a total line and the
// price label.
func FormatSummary(rows []Row, label string) string {
	var b strings.Builder
	if len(rows) == 0 {
		b.WriteString("No usage recorded in this period.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "%-10s  %-22s  %7s  %9s  %9s  %8s  %10s\n", "day", "model", "renders", "audio s", "stt calls", "stt s", "est. USD")
	var t Row
	for _, r := range rows {
		b.WriteString(formatRow(r))
		t.Renders += r.Renders
		t.AudioSeconds += r.AudioSeconds
		t.STTCalls += r.STTCalls
		t.STTSeconds += r.STTSeconds
		t.EstimateUSD += r.EstimateUSD
		t.Unpriced += r.Unpriced
	}
	t.Day, t.Model = "total", ""
	b.WriteString(formatRow(t))
	fmt.Fprintf(&b, "Estimates: %s.\n", label)
	// The ledger records tokens, not durations (OBS-03), so audio seconds are derived.
	fmt.Fprintf(&b, "Audio seconds are estimated from output audio tokens at %d per second.\n", AudioTokensPerSecond)
	if t.Unpriced > 0 {
		fmt.Fprintf(&b, "Not in the estimate (no list-price rate): %d ledger lines.\n", t.Unpriced)
	}
	return b.String()
}

func formatRow(r Row) string {
	return fmt.Sprintf("%-10s  %-22s  %7d  %9s  %9d  %8d  %10s\n", r.Day, r.Model, r.Renders,
		strconv.FormatFloat(r.AudioSeconds, 'f', 1, 64), r.STTCalls, r.STTSeconds, strconv.FormatFloat(r.EstimateUSD, 'f', 4, 64))
}
