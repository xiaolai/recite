package spoken_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/text"
	"github.com/xiaolai/recite/internal/verify"
)

// evidenceID identifies a live qualification's evidence (plan §1.4): the complete ordered
// corpus as rendered and verified, the attempt limits, the verify settings, the session, the
// canonicaliser and guard versions, and the implementation revision. Shard results are kept
// and aggregated per ID, so a changed corpus item, setting or build never reuses old results.
func evidenceID(tasks []pipeline.Task, opts verify.Options, spec realtime.SessionSpec, revision string) string {
	type chunk struct {
		ID, Spoken, Lang string
		Certain          bool
		Estimate         time.Duration
		RequestLike      bool
	}
	id := struct {
		Chunks               []chunk
		Mode                 verify.Mode
		Thresholds           verify.Thresholds
		MaxAttempts, Extra   int
		Spec                 realtime.SessionSpec
		CanonVersion, Guards int
		Revision             string
	}{Mode: opts.Mode, Thresholds: opts.Mode.Thresholds(), MaxAttempts: opts.MaxAttempts,
		Extra: opts.RequestExtraAttempts, Spec: spec, CanonVersion: verify.CanonVersion,
		Guards: verify.GuardVersion, Revision: revision}
	for _, tk := range tasks {
		c := tk.Chunk
		id.Chunks = append(id.Chunks, chunk{c.ID, c.Spoken, c.Language.Code, c.Language.Certain, c.Estimate, c.RequestLike})
	}
	data, err := json.Marshal(id)
	if err != nil {
		panic(err) // plain data: cannot fail
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:8])
}

func TestEvidenceIDCoversCorpusSettingsAndRevision(t *testing.T) {
	tasks := func(items ...string) []pipeline.Task {
		var ts []pipeline.Task
		for i, s := range items {
			ts = append(ts, pipeline.Task{Chunk: verify.Chunk{ID: fmt.Sprintf("c%04d", i+1), Spoken: s,
				Language: text.Language{Code: "en"}, Estimate: time.Second}})
		}
		return ts
	}
	opts := verify.OptionsFor(verify.ModeNormal)
	spec := realtime.SessionSpec{Model: "gpt-realtime-2.1", Voice: "marin", Speed: 1}
	base := evidenceID(tasks("one", "two"), opts, spec, "r1")
	if again := evidenceID(tasks("one", "two"), opts, spec, "r1"); again != base {
		t.Fatal("the same evidence gave two IDs")
	}
	strict := opts
	strict.Mode = verify.ModeStrict
	fewer := opts
	fewer.MaxAttempts = 2
	slower := spec
	slower.Speed = 0.9
	for name, id := range map[string]string{
		"a changed probe item": evidenceID(tasks("one", "three"), opts, spec, "r1"),
		"reordered items":      evidenceID(tasks("two", "one"), opts, spec, "r1"),
		"an added item":        evidenceID(tasks("one", "two", "three"), opts, spec, "r1"),
		"the verify mode":      evidenceID(tasks("one", "two"), strict, spec, "r1"),
		"the attempt limit":    evidenceID(tasks("one", "two"), fewer, spec, "r1"),
		"the session":          evidenceID(tasks("one", "two"), opts, slower, "r1"),
		"the revision":         evidenceID(tasks("one", "two"), opts, spec, "r2"),
	} {
		if id == base {
			t.Errorf("%s kept the evidence ID", name)
		}
	}
}
