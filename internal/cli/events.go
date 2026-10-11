package cli

import (
	"encoding/json"
	"io"
	"math"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/logx"
	"github.com/xiaolai/recite/internal/verify"
)

// The `say --json` events of design §12.5 (CLI-06). docs/schema/events.schema.json is their
// contract; a change here is a change there, with its version raised.

// planEvent opens a run, and is the whole report of --dry-run (RUN-06).
type planEvent struct {
	Event          string   `json:"event"`
	DryRun         bool     `json:"dry_run"`
	Chunks         int      `json:"chunks"`
	Cached         int      `json:"cached"`
	EstSeconds     float64  `json:"est_seconds"`
	EstWallSeconds float64  `json:"est_wall_seconds"`
	Jobs           int      `json:"jobs"`
	EstimateUSD    *float64 `json:"estimate_usd"`
	PriceNote      string   `json:"price_note"`
}

// chunkEvent is one step of a chunk: each take rendered or rejected, then its final state.
type chunkEvent struct {
	Event   string   `json:"event"`
	ID      string   `json:"id"`
	State   string   `json:"state"`
	Attempt int      `json:"attempt,omitempty"`
	Seconds *float64 `json:"seconds,omitempty"`
	Score   *float64 `json:"score,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	Cached  bool     `json:"cached,omitempty"`
}

// doneEvent is always the last line: the exit code, and the output when one was written.
type doneEvent struct {
	Event     string   `json:"event"`
	Exit      int      `json:"exit"`
	Output    string   `json:"output,omitempty"`
	DurationS *float64 `json:"duration_s,omitempty"`
}

// Chunk states of chunkEvent beyond the verify.State values.
const (
	chunkRendered = "rendered"
	chunkRejected = "rejected"
)

// event writes one NDJSON line. A failed write is logged: stdout is gone, and the exit code
// still reports the run.
func (c *CLI) event(v any) {
	b, err := json.Marshal(v)
	if err == nil {
		_, err = io.WriteString(c.Stdout, logx.Scrub(string(b))+"\n")
	}
	if err != nil {
		c.Logger.Warn("event not written", "err", err)
	}
}

// chunkEvents are the events of a delivered chunk, in order: per attempt, rendered and (when
// it was rejected) rejected; then the final state. A cached chunk has only its final state.
func chunkEvents(r verify.ChunkResult) []chunkEvent {
	var out []chunkEvent
	if !r.Cached {
		for _, a := range r.Attempts {
			ev := chunkEvent{Event: "chunk", ID: r.ChunkID, State: chunkRendered, Attempt: a.N}
			if len(a.PCM) > 0 {
				ev.Seconds = ptr(round3(a.PCM.Duration().Seconds()))
			}
			out = append(out, ev)
			if a.Rejection != nil {
				out = append(out, chunkEvent{Event: "chunk", ID: r.ChunkID, State: chunkRejected, Attempt: a.N, Reason: string(a.Rejection.Reason)})
			}
		}
	}
	final := chunkEvent{Event: "chunk", ID: r.ChunkID, State: string(r.State), Attempt: r.Final, Cached: r.Cached}
	if r.State != verify.StateNoEligible {
		final.Score = ptr(round3(r.Score))
		final.Seconds = ptr(round3(float64(len(r.PCM)) / audio.BytesPerSecond))
	}
	return append(out, final)
}

func ptr[T any](v T) *T { return &v }

// round3 keeps three decimals, so the events stay short and stable.
func round3(x float64) float64 { return math.Round(x*1000) / 1000 }

// sayDone emits the done event of a `say` run under --json and returns code.
func (c *CLI) sayDone(code int) int {
	if !c.globals.JSON {
		return code
	}
	ev := doneEvent{Event: "done", Exit: code}
	if o := c.written; o != nil {
		ev.Output = o.Path
		ev.DurationS = ptr(round3(o.DurationS))
	}
	c.event(ev)
	return code
}
