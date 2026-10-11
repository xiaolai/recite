package calibrate

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/xiaolai/recite/internal/verify"
)

// Report is everything the calibration report states.
type Report struct {
	Date       time.Time
	Source     string // the corpus and where it comes from
	Seed       uint64
	Mode       verify.Mode
	Thresholds verify.Thresholds
	Propose    []Record // the 60% part: thresholds may be proposed on it only
	HeldOut    Evaluation
}

// ReportName is the report's file name for date: calibration-YYYYMMDD.md.
func ReportName(date time.Time) string { return "calibration-" + date.Format("20060102") + ".md" }

// WriteReport writes the markdown report: method, human labels, per-type results with Wilson
// 95% intervals, the said guard's catch rate, construction-vs-human disagreements and the
// held-out verdict.
func WriteReport(w io.Writer, r Report) error {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	held := make([]Record, len(r.HeldOut.Rows))
	for i, row := range r.HeldOut.Rows {
		held[i] = row.Record
	}
	verdict := "PASS"
	if err := r.HeldOut.Check(); err != nil {
		verdict = "FAIL: " + strings.TrimPrefix(err.Error(), ErrCriteria.Error()+": ")
	}

	p("# Verifier calibration, %s (VER-07 mini calibration)\n\n", r.Date.Format("2006-01-02"))
	p("**Held-out verdict: %s**\n\n", verdict)
	p("## Method\n\n")
	p("- Corpus: %s.\n", r.Source)
	p("- Candidates: %d good (the passage rendered as is) and %d corrupted (an edited passage rendered: %s), built with seed %d.\n",
		count(r.Propose, held, false), count(r.Propose, held, true), kindList(), r.Seed)
	p("- Every candidate is rendered once and verified against the **original** passage, mode `%s` (accept at or above %.2f, reject below %.2f).\n",
		r.Mode, r.Thresholds.Accept, r.Thresholds.Reject)
	p("- The said guard is bypassed so every take reaches STT; the run fails unless every take was transcribed. Its would-be catch rate is reported separately below.\n")
	p("- Split 60/40, stratified by construction kind and seeded: %d proposal samples, %d held out. Thresholds may be proposed on the proposal part only.\n",
		len(r.Propose), len(held))
	p("- Held-out labels come from listening: the owner heard each clip under an anonymised ID and judged whether it says the original passage exactly. Construction labels are used only to stratify.\n")
	p("- Criteria (VER-07): 0 false accepts on clips labelled corrupted; at most %.0f%% false rejects on clips labelled good. Intervals are Wilson 95%%.\n\n", MaxFalseRejectRate*100)

	p("## Held-out criteria\n\n| Measure | Count | Rate | Wilson 95%% |\n|---|---|---|---|\n")
	rateRow(p, "False accepts (of clips labelled corrupted)", r.HeldOut.FalseAccepts, r.HeldOut.Corrupted)
	rateRow(p, "False rejects (of clips labelled good)", r.HeldOut.FalseRejects, r.HeldOut.Good)
	p("\n")

	p("## Human labels (held out)\n\n| Clip | Construction | Human label | Verifier | Score | Heard | Rejection |\n|---|---|---|---|---|---|---|\n")
	for _, row := range r.HeldOut.Rows {
		v := "rejected"
		if row.Accepted {
			v = "accepted"
		}
		p("| %s | %s | %s | %s | %.4f | %s | %s |\n", row.ID, row.Kind, row.Human, v, row.Score, cell(strings.Join(row.Heard, " / ")), cell(row.Rejection))
	}
	p("\n")

	p("## Per-type results (held out)\n\n")
	kindTable(p, ByKind(held))
	p("## Per-type results (proposal part)\n\n")
	kindTable(p, ByKind(r.Propose))

	p("## Said guard (bypassed; reported separately)\n\n| Kind | Would have caught | Rate | Wilson 95%% |\n|---|---|---|---|\n")
	for _, k := range ByKind(append(append([]Record(nil), r.Propose...), held...)) {
		lo, hi := k.SaidInterval()
		p("| %s | %d of %d | %s | %.2f–%.2f |\n", k.Kind, k.SaidMismatch, k.N, pct(k.SaidMismatch, k.N), lo, hi)
	}
	p("\n")

	p("## Construction vs human disagreements\n\n")
	if d := r.HeldOut.Disagreements(); len(d) == 0 {
		p("None: every held-out label agrees with its construction.\n\n")
	} else {
		p("| Clip | Construction | Human label | Rendered text |\n|---|---|---|---|\n")
		for _, row := range d {
			p("| %s | %s | %s | %s |\n", row.ID, row.Kind, row.Human, cell(row.Rendered))
		}
		p("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func kindTable(p func(string, ...any), ks []KindResult) {
	p("| Kind | Accepted | Rate | Wilson 95%% | Scores |\n|---|---|---|---|---|\n")
	for _, k := range ks {
		lo, hi := k.AcceptInterval()
		p("| %s | %d of %d | %s | %.2f–%.2f | %.4f–%.4f |\n", k.Kind, k.Accepted, k.N, pct(k.Accepted, k.N), lo, hi, k.MinScore, k.MaxScore)
	}
	p("\n")
}

func rateRow(p func(string, ...any), name string, k, n int) {
	lo, hi := Wilson(k, n)
	p("| %s | %d of %d | %s | %.2f–%.2f |\n", name, k, n, pct(k, n), lo, hi)
}

func pct(k, n int) string {
	if n == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(k)/float64(n))
}

func count(a, b []Record, corrupted bool) int {
	n := 0
	for _, r := range append(append([]Record(nil), a...), b...) {
		if r.Corrupted() == corrupted {
			n++
		}
	}
	return n
}

func kindList() string {
	s := make([]string, len(Corruptions))
	for i, k := range Corruptions {
		s[i] = string(k)
	}
	return strings.Join(s, ", ")
}

// cell makes text safe inside a markdown table cell.
func cell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s)
}
