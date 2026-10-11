package calibrate

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// MaxFalseRejectRate is the held-out limit on false rejects of clips labelled good (VER-07).
const MaxFalseRejectRate = 0.05

// Row is one held-out clip with its human label.
type Row struct {
	Record
	Human Label
}

// FalseAccept is a take the verifier accepted that the listener labelled corrupted.
func (r Row) FalseAccept() bool { return r.Accepted && r.Human == LabelCorrupted }

// FalseReject is a take the verifier rejected that the listener labelled good.
func (r Row) FalseReject() bool { return !r.Accepted && r.Human == LabelGood }

// Disagrees reports whether the listener's label differs from the construction label.
func (r Row) Disagrees() bool { return r.Corrupted() != (r.Human == LabelCorrupted) }

// KindResult is the verdicts on one construction kind.
type KindResult struct {
	Kind               Kind
	N, Accepted        int
	SaidMismatch       int // takes the bypassed said guard would have rejected
	MinScore, MaxScore float64
}

// AcceptInterval is the Wilson 95% interval of the acceptance rate.
func (k KindResult) AcceptInterval() (lo, hi float64) { return Wilson(k.Accepted, k.N) }

// SaidInterval is the Wilson 95% interval of the said guard's catch rate.
func (k KindResult) SaidInterval() (lo, hi float64) { return Wilson(k.SaidMismatch, k.N) }

// ByKind summarises records per construction kind, good first, then Corruptions order.
func ByKind(recs []Record) []KindResult {
	var out []KindResult
	for _, k := range append([]Kind{Good}, Corruptions...) {
		r := KindResult{Kind: k}
		for _, x := range recs {
			if x.Kind != k {
				continue
			}
			if r.N == 0 || x.Score < r.MinScore {
				r.MinScore = x.Score
			}
			r.MaxScore = max(r.MaxScore, x.Score)
			r.N++
			if x.Accepted {
				r.Accepted++
			}
			if x.SaidMismatch {
				r.SaidMismatch++
			}
		}
		if r.N > 0 {
			out = append(out, r)
		}
	}
	return out
}

// Evaluation is the held-out result against human labels.
type Evaluation struct {
	Rows                       []Row // ID order
	Good, Corrupted            int   // clips labelled good, corrupted
	FalseAccepts, FalseRejects int
}

// Evaluate joins the held-out records with the listener's labels. Every held-out clip must be
// labelled, and only those.
func Evaluate(heldOut []Record, labels map[string]Label) (Evaluation, error) {
	var ev Evaluation
	var missing []string
	seen := map[string]bool{}
	for _, r := range heldOut {
		seen[r.ID] = true
		l, ok := labels[r.ID]
		if !ok {
			missing = append(missing, r.ID)
			continue
		}
		row := Row{Record: r, Human: l}
		ev.Rows = append(ev.Rows, row)
		if l == LabelGood {
			ev.Good++
		} else {
			ev.Corrupted++
		}
		if row.FalseAccept() {
			ev.FalseAccepts++
		}
		if row.FalseReject() {
			ev.FalseRejects++
		}
	}
	var extra []string
	for id := range labels {
		if !seen[id] {
			extra = append(extra, id)
		}
	}
	slices.Sort(extra)
	if len(missing) > 0 || len(extra) > 0 {
		return ev, fmt.Errorf("calibrate: labels: unlabelled held-out clips %v, labels for unknown clips %v", missing, extra)
	}
	slices.SortFunc(ev.Rows, func(a, b Row) int { return strings.Compare(a.ID, b.ID) })
	return ev, nil
}

// Disagreements are the held-out clips whose human label differs from their construction.
func (e Evaluation) Disagreements() []Row {
	var out []Row
	for _, r := range e.Rows {
		if r.Disagrees() {
			out = append(out, r)
		}
	}
	return out
}

// ErrCriteria means the held-out evaluation fails VER-07's criteria.
var ErrCriteria = errors.New("calibrate: held-out criteria not met")

// Check applies the held-out criteria: no false accept on a clip labelled corrupted, and at
// most 5% false rejects on clips labelled good. Both label classes must be present, or the
// rates are not measured.
func (e Evaluation) Check() error {
	var why []string
	if e.Good == 0 || e.Corrupted == 0 {
		why = append(why, fmt.Sprintf("%d clips labelled good and %d corrupted: both are needed", e.Good, e.Corrupted))
	}
	if e.FalseAccepts > 0 {
		why = append(why, fmt.Sprintf("%d false accepts of %d clips labelled corrupted (want 0)", e.FalseAccepts, e.Corrupted))
	}
	if e.Good > 0 && float64(e.FalseRejects) > MaxFalseRejectRate*float64(e.Good) {
		why = append(why, fmt.Sprintf("%d false rejects of %d clips labelled good (want at most %.0f%%)", e.FalseRejects, e.Good, MaxFalseRejectRate*100))
	}
	if len(why) > 0 {
		return fmt.Errorf("%w: %s", ErrCriteria, strings.Join(why, "; "))
	}
	return nil
}
