package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var ledgerNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

const helperEnv = "RECITE_TEST_LEDGER_HELPER"

// TestMain lets the test binary act as a second appending process.
func TestMain(m *testing.M) {
	if dir := os.Getenv(helperEnv); dir != "" {
		os.Exit(appendHelper(dir))
	}
	os.Exit(m.Run())
}

// bigEntry is an entry whose line is several KB, so a split write would show.
func bigEntry(writer string, i int) Entry {
	return Entry{Time: ledgerNow, Command: "say", Kind: KindRender, Model: strings.Repeat("m", 4000),
		Chunk: fmt.Sprintf("%s-%04d", writer, i), Attempt: 1, OutputAudioTokens: i}
}

func appendHelper(dir string) int {
	l, err := Open(dir, func() time.Time { return ledgerNow })
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	id := os.Getenv(helperEnv + "_ID")
	for i := range 200 {
		if err := l.Append(bigEntry(id, i)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return 0
}

// readLines decodes every ledger line strictly: a torn or interleaved line fails.
func readLines(t *testing.T, path string) []Entry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e Entry
		dec := json.NewDecoder(strings.NewReader(sc.Text()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil || dec.InputOffset() != int64(len(sc.Text())) {
			t.Fatalf("line %d is not one whole entry: %v", len(out)+1, err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Many goroutines and several processes append at once; every line is whole and every entry
// is there exactly once.
// accept: OBS-03#1
func TestConcurrentAppendsNeverInterleave(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	l, err := Open(dir, func() time.Time { return ledgerNow })
	if err != nil {
		t.Fatal(err)
	}
	const procs, goroutines, each = 4, 32, 50
	cmds := make([]*exec.Cmd, procs)
	for p := range procs {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), helperEnv+"="+dir, helperEnv+"_ID=p"+strconv.Itoa(p))
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[p] = cmd
	}
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*each)
	for g := range goroutines {
		wg.Go(func() {
			for i := range each {
				if err := l.Append(bigEntry("g"+strconv.Itoa(g), i)); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper process: %v", err)
		}
	}
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range readLines(t, l.Path()) {
		if seen[e.Chunk] {
			t.Fatalf("%s twice", e.Chunk)
		}
		seen[e.Chunk] = true
	}
	if want := procs*200 + goroutines*each; len(seen) != want {
		t.Fatalf("%d entries, want %d", len(seen), want)
	}
	fi, err := os.Stat(l.Path())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("ledger mode %v %v", fi.Mode(), err)
	}
	di, err := os.Stat(dir)
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("ledger directory mode %v %v", di.Mode(), err)
	}
}

// A fixture's estimate matches a hand calculation, and every estimate is labelled.
// accept: OBS-03#1
func TestPriceEstimateMatchesHandCalculation(t *testing.T) {
	p := ListPrices
	// 355 audio tokens of gpt-realtime-2.1 at $64 / 1M: 355 × 64 / 1,000,000 = 0.02272.
	r := p.RenderEntry(Entry{Model: "gpt-realtime-2.1"}, Tokens{Input: 120, Output: 435, InputText: 118, OutputText: 80, OutputAudio: 355, Detailed: true})
	// 28 s of gpt-transcribe at $0.0045 / min: 28 / 60 × 0.0045 = 0.0021.
	s := p.STTEntry(Entry{Model: "gpt-transcribe"}, 28)
	// 1,000 audio tokens of the mini at $20 / 1M = 0.02; without details all output counts.
	m := p.RenderEntry(Entry{Model: "gpt-realtime-2.1-mini"}, Tokens{Output: 1000})
	for _, c := range []struct {
		e    Entry
		want float64
	}{{r, 0.02272}, {s, 0.0021}, {m, 0.02}} {
		if c.e.EstimateUSD == nil || math.Abs(*c.e.EstimateUSD-c.want) > 1e-12 {
			t.Errorf("%s %s: estimate %v, want %v", c.e.Kind, c.e.Model, c.e.EstimateUSD, c.want)
		}
		if !strings.HasPrefix(c.e.PriceNote, "list price, not billed") || !strings.Contains(c.e.PriceNote, p.Date) {
			t.Errorf("price note %q", c.e.PriceNote)
		}
	}
	if p.Date == "" || p.Source == "" {
		t.Fatal("the price table has no dated source")
	}
	// A model without a recorded rate carries no estimate rather than a made-up one.
	if e := p.STTEntry(Entry{Model: "gpt-live-transcribe"}, 30); e.EstimateUSD != nil {
		t.Fatalf("unpriced model estimated at %v", *e.EstimateUSD)
	}
}

// A ledger last written in an earlier month is rotated to that month's archive.
func TestLedgerRotatesMonthly(t *testing.T) {
	dir := t.TempDir()
	now := ledgerNow
	l, err := Open(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Entry{Chunk: "september"}); err != nil {
		t.Fatal(err)
	}
	sept := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)
	if err := os.Chtimes(l.Path(), sept, sept); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Entry{Chunk: "october"}); err != nil {
		t.Fatal(err)
	}
	old := readLines(t, filepath.Join(dir, "usage-2026-09.jsonl"))
	cur := readLines(t, l.Path())
	if len(old) != 1 || old[0].Chunk != "september" || len(cur) != 1 || cur[0].Chunk != "october" {
		t.Fatalf("archive %+v, current %+v", old, cur)
	}
	// Same month: no rotation.
	if err := l.Append(Entry{Chunk: "october-2"}); err != nil {
		t.Fatal(err)
	}
	if n := len(readLines(t, l.Path())); n != 2 {
		t.Fatalf("%d lines after a same-month append", n)
	}
	// A clock behind the ledger's month never rotates it.
	now = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if err := l.Append(Entry{Chunk: "skewed"}); err != nil {
		t.Fatal(err)
	}
	if n := len(readLines(t, l.Path())); n != 3 {
		t.Fatalf("%d lines after an append with the clock behind", n)
	}
}

// A symlink in place of the ledger is refused, not followed.
func TestLedgerRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, func() time.Time { return ledgerNow })
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Symlink(dest, l.Path()); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Entry{}); err == nil {
		t.Fatal("appended through a symlink")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("symlink destination touched: %v", err)
	}
}

func TestEstimateSpeechMatchesHandCalculation(t *testing.T) {
	// 60 s → 1200 audio tokens at $64/1M = 0.0768, plus one minute of gpt-transcribe 0.0045.
	got, ok := ListPrices.EstimateSpeech("gpt-realtime-2.1", "gpt-transcribe", 60)
	if !ok || math.Abs(got-0.0813) > 1e-9 {
		t.Fatalf("EstimateSpeech = %v, %v; want 0.0813", got, ok)
	}
	if got, ok := ListPrices.EstimateSpeech("gpt-realtime-2.1", "", 60); !ok || math.Abs(got-0.0768) > 1e-9 {
		t.Fatalf("without STT = %v, %v; want 0.0768", got, ok)
	}
	if _, ok := ListPrices.EstimateSpeech("unknown-model", "gpt-transcribe", 60); ok {
		t.Fatal("a model without a rate was priced")
	}
}
