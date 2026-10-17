package calibrate_test

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/calibrate"
)

// verdictLine is the report's held-out verdict as WriteReport states it.
var verdictLine = regexp.MustCompile(`(?m)^\*\*Held-out verdict( \(machine-labelled\))?: (.+)\*\*$`)

// machineProvenance is the method line of a machine-labelled report; it must name the labeller.
var machineProvenance = regexp.MustCompile(`(?m)^- Held-out labels are machine labels from (\S.*?), not listening: they are weaker evidence than human listening, and a human labelling pass supersedes them\.`)

// The Method lines that identify the held-out inventory: corpus, candidate counts and seed, split sizes.
var (
	titleScript = regexp.MustCompile(`(?m)^# Verifier calibration, \S+ \(VER-11 full calibration, (\w+)\)$`)
	corpusLine  = regexp.MustCompile(`(?m)^- Corpus: ([^:]+):`)
	candLine    = regexp.MustCompile(`(?m)^- Candidates: (\d+) good .* and (\d+) corrupted .*, built with seed (\d+)\.$`)
	splitLine   = regexp.MustCompile(`(?m)^- Split 60/40, .*: (\d+) proposal samples, (\d+) held out\.`)
)

const humanProvenance = "- Held-out labels come from listening:"

// countCell is a "k of n" table cell.
var countCell = regexp.MustCompile(`^(\d+) of (\d+)$`)

// The committed calibration reports exist and each states its held-out verdict as measured,
// with the method, the per-type results and Wilson intervals. Under D23 the verdict is
// reported, not gated: a FAIL verdict is valid evidence about the opt-in STT mode. The
// evidence is checked, not just the headings: the label table is re-evaluated with the
// package's own criteria, and the verdict, counts, rates and intervals must follow from it.
// accept: VER-07#2
// accept: VER-07#3
// accept: VER-11#1
func TestCommittedReportsStateVerdict(t *testing.T) {
	docs := filepath.Join("..", "..", "docs")
	for _, pattern := range []string{"calibration-[0-9]*[0-9].md", "calibration-*-latin.md", "calibration-*-han.md"} {
		paths, err := filepath.Glob(filepath.Join(docs, pattern))
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) == 0 {
			t.Fatalf("no committed report matches docs/%s", pattern)
		}
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			t.Run(filepath.Base(p), func(t *testing.T) { checkCommittedReport(t, string(data)) })
		}
	}
}

// checkCommittedReport checks one report's verdict, provenance and numbers against its label table.
func checkCommittedReport(t *testing.T, s string) {
	t.Helper()
	for _, want := range []string{"## Method", "## Held-out criteria", "## Per-type results (held out)", "Wilson 95%"} {
		if !strings.Contains(s, want) {
			t.Errorf("lacks %q", want)
		}
	}
	m := verdictLine.FindStringSubmatch(s)
	if m == nil {
		t.Fatal("no held-out verdict line")
	}
	machine := m[1] != ""
	labelHead := "Human labels (held out)"
	if machine {
		labelHead = "Machine labels (held out)"
		if pm := machineProvenance.FindStringSubmatch(s); pm == nil || strings.TrimSpace(pm[1]) == "" {
			t.Error("a machine-labelled report does not name its labeller")
		}
		if strings.Contains(s, humanProvenance) {
			t.Error("a machine-labelled report claims listening")
		}
	} else if !strings.Contains(s, humanProvenance) || machineProvenance.MatchString(s) {
		t.Error("a human-labelled report lacks the listening provenance line")
	}

	labelRows := tableRows(t, s, labelHead, 7)
	checkHeldOutInventory(t, s, labelRows)
	ev := evaluateLabelTable(t, labelRows)
	want := "PASS"
	if err := ev.Check(); err != nil {
		want = "FAIL: " + strings.TrimPrefix(err.Error(), calibrate.ErrCriteria.Error()+": ")
	}
	if m[2] != want {
		t.Errorf("verdict %q, the label table gives %q", m[2], want)
	}

	criteria := tableRows(t, s, "Held-out criteria", 4)
	wantCriteria := map[string][2]int{
		"False accepts (of clips labelled corrupted)": {ev.FalseAccepts, ev.Corrupted},
		"False rejects (of clips labelled good)":      {ev.FalseRejects, ev.Good},
	}
	if len(criteria) != len(wantCriteria) {
		t.Fatalf("held-out criteria table has %d rows, want %d", len(criteria), len(wantCriteria))
	}
	seen := map[string]bool{}
	for _, row := range criteria {
		kn, ok := wantCriteria[row[0]]
		if !ok {
			t.Errorf("unknown held-out criterion %q", row[0])
			continue
		}
		if seen[row[0]] {
			t.Errorf("held-out criterion %q appears twice", row[0])
			continue
		}
		seen[row[0]] = true
		checkRate(t, row[0], row[1:], kn[0], kn[1])
	}

	held := make([]calibrate.Record, len(ev.Rows))
	for i, r := range ev.Rows {
		held[i] = r.Record
	}
	kinds := calibrate.ByKind(held)
	perKind := tableRows(t, s, "Per-type results (held out)", 5)
	if len(perKind) != len(kinds) {
		t.Fatalf("held-out per-type table has %d rows, the label table gives %d kinds", len(perKind), len(kinds))
	}
	for i, k := range kinds {
		row := perKind[i]
		if row[0] != string(k.Kind) {
			t.Errorf("held-out per-type row %d is %q, want %q", i, row[0], k.Kind)
			continue
		}
		checkRate(t, row[0], row[1:4], k.Accepted, k.N)
		if got := fmt.Sprintf("%.4f–%.4f", k.MinScore, k.MaxScore); row[4] != got {
			t.Errorf("%s: scores %q, the label table gives %q", row[0], row[4], got)
		}
	}
}

// checkHeldOutInventory rebuilds the run's samples offline from the Method section (corpus
// files, script, candidate counts, seed) with the package's BuildScript and Split, and requires
// the label table to hold exactly the held-out clips the harness produced, with their
// constructions, and the declared split sizes to match. Without it, deleting unfavourable label
// rows and updating the summaries would still recompute to a consistent verdict.
func checkHeldOutInventory(t *testing.T, s string, rows [][]string) {
	t.Helper()
	sc := calibrate.Latin
	if m := titleScript.FindStringSubmatch(s); m != nil {
		var err error
		if sc, err = calibrate.ParseScript(m[1]); err != nil {
			t.Fatal(err)
		}
	}
	cm := corpusLine.FindStringSubmatch(s)
	km := candLine.FindStringSubmatch(s)
	sm := splitLine.FindStringSubmatch(s)
	if cm == nil || km == nil || sm == nil {
		t.Fatal("the Method section lacks the corpus, candidates or split line")
	}
	// "dir/a.txt then b.txt": later files are named relative to the first one's directory.
	files := strings.Split(cm[1], " then ")
	var passages []byte
	for i, f := range files {
		if i > 0 {
			f = filepath.Join(filepath.Dir(files[0]), f)
		}
		b, err := os.ReadFile(filepath.Join("..", "..", f))
		if err != nil {
			t.Fatalf("corpus: %v", err)
		}
		passages = append(append(passages, b...), '\n')
	}
	corpus, err := calibrate.ReadPassages(bytes.NewReader(passages))
	if err != nil {
		t.Fatal(err)
	}
	atoi := func(v string) int {
		t.Helper()
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	seed, err := strconv.ParseUint(km[3], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	samples, err := calibrate.BuildScript(sc, corpus, atoi(km[1]), atoi(km[2]), seed)
	if err != nil {
		t.Fatal(err)
	}
	propose, heldOut := calibrate.Split(samples, seed)
	if atoi(sm[1]) != len(propose) || atoi(sm[2]) != len(heldOut) {
		t.Errorf("Method declares %s proposal and %s held-out samples, the harness gives %d and %d", sm[1], sm[2], len(propose), len(heldOut))
	}
	if len(rows) != len(heldOut) {
		t.Errorf("the label table has %d rows, the harness holds out %d clips", len(rows), len(heldOut))
	}
	want := map[string]calibrate.Kind{}
	for _, h := range heldOut {
		want[h.ID] = h.Kind
	}
	for _, row := range rows {
		k, ok := want[row[0]]
		switch {
		case !ok:
			t.Errorf("clip %s is not a held-out clip of this run", row[0])
		case string(k) != row[1]:
			t.Errorf("clip %s: construction %q, the harness built %q", row[0], row[1], k)
		}
		delete(want, row[0])
	}
	missing := make([]string, 0, len(want))
	for id := range want {
		missing = append(missing, id)
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Errorf("held-out clips missing from the label table: %v", missing)
	}
}

// checkRate checks a "k of n", rate, Wilson 95% triple against k and n, to the printed precision.
func checkRate(t *testing.T, name string, cells []string, k, n int) {
	t.Helper()
	m := countCell.FindStringSubmatch(cells[0])
	if m == nil {
		t.Errorf("%s: count %q is not \"k of n\"", name, cells[0])
		return
	}
	if gk, _ := strconv.Atoi(m[1]); gk != k {
		t.Errorf("%s: count %d, the label table gives %d", name, gk, k)
	}
	if gn, _ := strconv.Atoi(m[2]); gn != n {
		t.Errorf("%s: of %d, the label table gives %d", name, gn, n)
	}
	rate := "n/a"
	if n > 0 {
		rate = fmt.Sprintf("%.1f%%", 100*float64(k)/float64(n))
	}
	if cells[1] != rate {
		t.Errorf("%s: rate %q, want %q", name, cells[1], rate)
	}
	lo, hi := calibrate.Wilson(k, n)
	if want := fmt.Sprintf("%.2f–%.2f", lo, hi); cells[2] != want {
		t.Errorf("%s: Wilson 95%% %q, Wilson(%d, %d) is %q", name, cells[2], k, n, want)
	}
}

// evaluateLabelTable rebuilds the held-out evaluation from the label table's Clip,
// Construction, label, Verifier and Score columns with the package's own Evaluate.
func evaluateLabelTable(t *testing.T, rows [][]string) calibrate.Evaluation {
	t.Helper()
	if len(rows) == 0 {
		t.Fatal("the label table has no rows")
	}
	known := append([]calibrate.Kind{calibrate.Good}, calibrate.Corruptions...)
	var held []calibrate.Record
	labels := map[string]calibrate.Label{}
	for _, row := range rows {
		id, kind := row[0], calibrate.Kind(row[1])
		if !slices.Contains(known, kind) {
			t.Fatalf("clip %s: unknown construction %q", id, row[1])
		}
		label := calibrate.Label(row[2])
		if label != calibrate.LabelGood && label != calibrate.LabelCorrupted {
			t.Fatalf("clip %s: label %q", id, row[2])
		}
		if _, dup := labels[id]; dup {
			t.Fatalf("clip %s appears twice", id)
		}
		var accepted bool
		switch row[3] {
		case "accepted":
			accepted = true
		case "rejected":
		default:
			t.Fatalf("clip %s: verifier %q", id, row[3])
		}
		score, err := strconv.ParseFloat(row[4], 64)
		if err != nil {
			t.Fatalf("clip %s: score %q: %v", id, row[4], err)
		}
		if math.IsNaN(score) || score < 0 || score > 1 { // also rejects ±Inf
			t.Fatalf("clip %s: score %q is not within [0, 1]", id, row[4])
		}
		labels[id] = label
		held = append(held, calibrate.Record{Sample: calibrate.Sample{ID: id, Kind: kind}, Reached: true, Accepted: accepted, Score: score})
	}
	ev, err := calibrate.Evaluate(held, labels)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

// tableRows returns the body rows of the markdown table under "## heading", each with exactly
// cols cells. Escaped pipes inside a cell stay in the cell.
func tableRows(t *testing.T, s, heading string, cols int) [][]string {
	t.Helper()
	_, after, ok := strings.Cut(s, "\n## "+heading+"\n")
	if !ok {
		t.Fatalf("no section %q", heading)
	}
	const esc = "\x00"
	var rows [][]string
	for i, line := range strings.Split(strings.TrimLeft(after, "\n"), "\n") {
		if !strings.HasPrefix(line, "|") {
			break
		}
		if i < 2 { // header and separator
			continue
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(strings.ReplaceAll(line, `\|`, esc), "|"), "|")
		var cells []string
		for c := range strings.SplitSeq(inner, "|") {
			cells = append(cells, strings.ReplaceAll(strings.TrimSpace(c), esc, "|"))
		}
		if len(cells) != cols {
			t.Fatalf("%s: row %q has %d cells, want %d", heading, line, len(cells), cols)
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		t.Fatalf("section %q has no table rows", heading)
	}
	return rows
}
