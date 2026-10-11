package calibrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xiaolai/recite/internal/livebudget"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/text"
	"github.com/xiaolai/recite/internal/verify"
)

// Record is one sample's single take and its verdict: what was meant, said and heard, and why
// it was rejected.
type Record struct {
	Sample
	Reached      bool     `json:"reached"`  // transcribed at least once
	Accepted     bool     `json:"accepted"` // the verifier would have kept the take as verified
	Score        float64  `json:"score"`
	Heard        []string `json:"heard"`
	SaidMismatch bool     `json:"said_mismatch"` // the bypassed said guard would have rejected
	Rejection    string   `json:"rejection,omitempty"`
}

// Tasks makes one pipeline task per sample: the reference is the original passage in spoken
// form, the rendered text is the sample's text in spoken form. spokenForm is the text
// pipeline's normaliser (identity in tests). The duration estimate is the rendered text's, so
// the duration guard judges the take against what it was asked to say.
func Tasks(samples []Sample, spec realtime.SessionSpec, spokenForm func(string) (string, error)) ([]pipeline.Task, error) {
	out := make([]pipeline.Task, 0, len(samples))
	for _, s := range samples {
		ref, err := spokenForm(s.Original)
		if err != nil {
			return nil, fmt.Errorf("calibrate: %s original: %w", s.ID, err)
		}
		say, err := spokenForm(s.Rendered)
		if err != nil {
			return nil, fmt.Errorf("calibrate: %s rendered: %w", s.ID, err)
		}
		out = append(out, pipeline.Task{
			Chunk: verify.Chunk{
				ID: s.ID, Spoken: ref,
				Language: text.Resolve(ref, text.ResolveOptions{}),
				Estimate: realtime.EstimateDuration(say, spec.Speed),
			},
			Spec:   spec,
			Render: say,
		})
	}
	return out, nil
}

// Options are the verification options of a calibration run: the given mode and trimming,
// one take per sample, and the said guard bypassed so every take reaches STT.
func Options(base verify.Options) verify.Options {
	o := base
	if o.Mode == "" {
		o.Mode = verify.ModeNormal
	}
	o.MaxAttempts, o.RequestExtraAttempts, o.BypassSaid = 1, 0, true
	return o
}

// Run renders and verifies every sample once through run (pipeline.Run, or a budget's wrap of
// it) and returns one record per sample, in sample order. keep, when set, receives each
// finished chunk with its audio. The run fails unless every sample reached STT (see
// CheckReached).
func Run(ctx context.Context, run livebudget.RunFunc, cfg pipeline.Config, samples []Sample, tasks []pipeline.Task,
	keep func(Sample, verify.ChunkResult) error) ([]Record, error) {
	if len(samples) != len(tasks) {
		return nil, fmt.Errorf("calibrate: %d samples, %d tasks", len(samples), len(tasks))
	}
	if run == nil {
		run = pipeline.Run
	}
	cfg.Verify = Options(cfg.Verify)
	recs := make([]Record, len(samples))
	cfg.Deliver = func(i int, r verify.ChunkResult) error {
		if r.ChunkID != samples[i].ID {
			return fmt.Errorf("calibrate: delivered %s at %s", r.ChunkID, samples[i].ID)
		}
		recs[i] = record(samples[i], r)
		if keep != nil {
			return keep(samples[i], r)
		}
		return nil
	}
	if _, err := run(ctx, cfg, tasks); err != nil {
		return recs, fmt.Errorf("calibrate: run: %w", err)
	}
	return recs, CheckReached(recs)
}

func record(s Sample, r verify.ChunkResult) Record {
	rec := Record{Sample: s, Score: r.Score, Accepted: r.State == verify.StateVerified}
	for _, a := range r.Attempts {
		rec.SaidMismatch = rec.SaidMismatch || a.SaidMismatch
		if a.Rejection != nil {
			rec.Rejection = string(a.Rejection.Reason) + ": " + a.Rejection.Detail
		}
		for _, t := range a.Transcriptions {
			rec.Reached = true
			rec.Heard = append(rec.Heard, t.Text)
		}
	}
	return rec
}

// ErrNotReached means a calibration run did not measure the verifier on every sample.
var ErrNotReached = errors.New("calibrate: not every sample reached STT")

// CheckReached asserts that every sample's take was transcribed, and that the transcriber
// was listening. A real recogniser sometimes hears a corrupted take as its original (it
// smooths "four the of nine" into "four of the nine"; the model may also read a passage it
// knows from memory), and those samples are what calibration measures: the owner's labels
// settle them. A transcriber that does not listen hears every corrupted take as the
// original, so the run fails when more than half of the corrupted takes are.
func CheckReached(recs []Record) error {
	var bad, original []string
	corrupted := 0
	for _, r := range recs {
		switch {
		case r.ID == "":
			bad = append(bad, "a sample was never delivered")
		case !r.Reached:
			bad = append(bad, fmt.Sprintf("%s (%s) was not transcribed: %s", r.ID, r.Kind, r.Rejection))
		case r.Corrupted() && !verify.Equivalent(r.Rendered, r.Original):
			corrupted++
			for _, h := range r.Heard {
				if verify.Equivalent(h, r.Original) {
					original = append(original, fmt.Sprintf("%s (%s) was rendered from edited text but heard as the original %q", r.ID, r.Kind, h))
					break
				}
			}
		}
	}
	if 2*len(original) > corrupted {
		bad = append(bad, fmt.Sprintf("%d of %d corrupted takes were heard as the original; the transcriber is not listening", len(original), corrupted))
		bad = append(bad, original...)
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w:\n  %s", ErrNotReached, strings.Join(bad, "\n  "))
	}
	return nil
}

// HeardAsOriginal lists the corrupted samples heard as their original passage, for the
// report: each is a false accept unless the owner labels its audio good.
func HeardAsOriginal(recs []Record) []Record {
	var out []Record
	for _, r := range recs {
		if !r.Corrupted() || verify.Equivalent(r.Rendered, r.Original) {
			continue
		}
		for _, h := range r.Heard {
			if verify.Equivalent(h, r.Original) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// Shard splits tasks, in order, into the fewest runs whose worst case (one take each) fits
// the audio budget.
func Shard(tasks []pipeline.Task, budget time.Duration) [][]pipeline.Task {
	var out [][]pipeline.Task
	var cur []pipeline.Task
	var est time.Duration
	for _, tk := range tasks {
		if len(cur) > 0 && livebudget.Preflight(est+tk.Chunk.Estimate, 1, budget) != nil {
			out, cur, est = append(out, cur), nil, 0
		}
		cur, est = append(cur, tk), est+tk.Chunk.Estimate
	}
	return append(out, cur)
}
