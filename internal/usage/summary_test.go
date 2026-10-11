package usage

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

func goldenFile(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run go test -update): %v", err)
	}
	if got != string(want) {
		t.Fatalf("%s mismatch\n--- got\n%s--- want\n%s", name, got, want)
	}
}

// accept: CLI-09#1
func TestSummaryGolden(t *testing.T) {
	res, err := Read(filepath.Join("testdata", "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 6 || res.Bad != 1 {
		t.Fatalf("read %d entries, %d bad; want 6 and 1", len(res.Entries), res.Bad)
	}
	goldenFile(t, "summary-all", FormatSummary(Summarize(res.Entries, time.Time{}), ListPrices.Label()))
	// --since 7d at 2026-10-12 12:00 UTC leaves out the September archive.
	since := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC).Add(-7 * 24 * time.Hour)
	goldenFile(t, "summary-7d", FormatSummary(Summarize(res.Entries, since), ListPrices.Label()))
}

func TestSummaryEmpty(t *testing.T) {
	res, err := Read(filepath.Join(t.TempDir(), "missing"))
	if err != nil || len(res.Entries) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if got := FormatSummary(Summarize(nil, time.Time{}), ListPrices.Label()); got != "No usage recorded in this period.\n" {
		t.Fatalf("%q", got)
	}
}
