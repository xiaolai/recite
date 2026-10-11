package pipeline_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.uber.org/goleak"

	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
	"github.com/xiaolai/recite/internal/stt"
)

// passFrom transcribes a take correctly only from attempt n on, so earlier attempts are
// rejected and retried.
type passFrom struct {
	*echo
	n int
}

func (p passFrom) Transcribe(ctx context.Context, pcm []byte, h stt.Hint) (stt.Transcript, error) {
	if _, attempt, ok := rtfake.PCMTag(pcm); !ok {
		return stt.Transcript{}, errors.New("passFrom: untagged take")
	} else if attempt < p.n {
		return stt.Transcript{Text: "something else entirely was heard here", Languages: []stt.DetectedLanguage{}}, nil
	}
	return p.echo.Transcribe(ctx, pcm, h)
}

// override is the per-response reasoning.effort the call puts on the wire: the
// response.create the real session builds from the request and the effort of the session it
// ran on (realtime.ResponseCreate). "" means no per-response reasoning.
func override(t *testing.T, c rtfake.RenderCall) string {
	t.Helper()
	body, err := realtime.ResponseCreate(realtime.ChunkRequest{
		ChunkID: c.Request.ChunkID, Attempt: c.Request.Attempt, Text: c.Request.Text, Instructions: "read",
		Effort: c.Request.Effort, SessionEffort: c.Spec.Effort,
	})
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Response struct {
			Reasoning *struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Response.Reasoning == nil {
		return ""
	}
	return sent.Response.Reasoning.Effort
}

// overrides runs ts until every chunk passes on attempt passAt and returns each call's
// override by chunk, in attempt order.
func overrides(t *testing.T, ts []pipeline.Task, passAt, jobs int) map[string][]string {
	t.Helper()
	synth := rtfake.NewScriptedSynthesizer()
	r := newRun(synth, passFrom{newEcho(ts), passAt}, jobs)
	if _, code, err := r.start(t, ts); err != nil || code != exitcode.OK {
		t.Fatalf("err %v, exit %d", err, code)
	}
	out := map[string][]string{}
	for _, c := range synth.Calls() {
		if c.Request.Attempt != len(out[c.Request.ChunkID])+1 {
			t.Fatalf("%s: attempt %d out of order", c.Request.ChunkID, c.Request.Attempt)
		}
		out[c.Request.ChunkID] = append(out[c.Request.ChunkID], override(t, c))
	}
	return out
}

func withEffort(ts []pipeline.Task, effort realtime.Effort, ladder ...realtime.Effort) []pipeline.Task {
	for i := range ts {
		ts[i].Spec.Effort = effort
		ts[i].Ladder = ladder
	}
	return ts
}

func TestEffectiveEffort(t *testing.T) {
	ladder := []realtime.Effort{"minimal", "low", "medium"}
	for attempt, want := range map[int]realtime.Effort{1: "minimal", 2: "low", 3: "medium", 4: "medium", 9: "medium"} {
		if got := realtime.EffectiveEffort("high", ladder, attempt); got != want {
			t.Errorf("attempt %d: %s, want %s", attempt, got, want)
		}
	}
	if got := realtime.EffectiveEffort("high", nil, 3); got != "high" {
		t.Errorf("no ladder: %s", got)
	}
}

// accept: SPK-11#1
func TestRetryEffortLadderOverrides(t *testing.T) {
	defer goleak.VerifyNone(t)
	ts := withEffort(tasks(1), "minimal", "minimal", "low", "medium")
	got := overrides(t, ts, 3, 1)["c0001"]
	if len(got) != 3 || got[0] != "" || got[1] != "low" || got[2] != "medium" {
		t.Fatalf("overrides %q, want [\"\" low medium]", got)
	}
}

// accept: SPK-11#2
func TestRetryEffortFirstAttemptBelowSession(t *testing.T) {
	defer goleak.VerifyNone(t)
	ts := withEffort(tasks(1), "high", "minimal", "low")
	got := overrides(t, ts, 3, 1)["c0001"]
	// The last entry repeats.
	if len(got) != 3 || got[0] != "minimal" || got[1] != "low" || got[2] != "low" {
		t.Fatalf("overrides %q, want [minimal low low]", got)
	}
}

// accept: SPK-11#3
func TestNoLadderNoOverride(t *testing.T) {
	defer goleak.VerifyNone(t)
	for _, e := range realtime.Efforts {
		ts := withEffort(tasks(6, "marin", "cedar"), e)
		for id, got := range overrides(t, ts, 2, 2) {
			for n, o := range got {
				if o != "" {
					t.Fatalf("effort %s: %s attempt %d carries reasoning %q", e, id, n+1, o)
				}
			}
		}
	}
}

// Two outputs (task groups) at different efforts share one pool key, so a session minted for
// one renders the other's chunks; each response must carry the override its own output needs.
// accept: SPK-11#4
func TestMixedEffortsSharePoolKey(t *testing.T) {
	defer goleak.VerifyNone(t)
	ts := tasks(12)
	want := map[string]realtime.Effort{}
	for i := range ts {
		ts[i].Spec.Effort = "minimal"
		if i%2 == 1 {
			ts[i].Spec.Effort = "high"
		}
		want[ts[i].Chunk.ID] = ts[i].Spec.Effort
	}
	synth := rtfake.NewScriptedSynthesizer()
	r := newRun(synth, passFrom{newEcho(ts), 1}, 1) // one session serves both groups
	if _, code, err := r.start(t, ts); err != nil || code != exitcode.OK {
		t.Fatalf("err %v, exit %d", err, code)
	}
	mixed := false
	for _, c := range synth.Calls() {
		w := want[c.Request.ChunkID]
		o := override(t, c)
		switch {
		case c.Spec.Effort == w && o != "":
			t.Errorf("%s on a %s session: needless override %q", c.Request.ChunkID, c.Spec.Effort, o)
		case c.Spec.Effort != w && o != string(w):
			t.Errorf("%s on a %s session: override %q, want %s", c.Request.ChunkID, c.Spec.Effort, o, w)
		}
		mixed = mixed || c.Spec.Effort != w
	}
	if !mixed {
		t.Fatal("no chunk ran on a session of the other output's effort; the test proves nothing")
	}
}
