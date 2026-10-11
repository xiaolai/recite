package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/xiaolai/recite/internal/logx"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/usage"
	"github.com/xiaolai/recite/internal/verify"
)

// realtimePerWorker is the throughput one render worker reaches, in seconds of audio per second
// of wall time, for the wall-time estimate (RUN-06). It is the measured 10.3–10.5× real time
// with 3 workers, verification included (dev-docs/bench/2026-10-11-live-throughput.md), not
// design §14.4's unverified 12× target.
const realtimePerWorker = 3.5

// estimate is what a planned run is expected to cost (RUN-06): the chunk count, the cache
// hits, the audio length, the wall time at --jobs and the list price of the renders left.
func (c *CLI) estimate(req SayRequest, job sayJob, dryRun bool) planEvent {
	ev := planEvent{Event: "plan", DryRun: dryRun, Chunks: len(job.tasks), Jobs: req.Jobs, PriceNote: usage.ListPrices.Label()}
	var total, left float64
	for _, t := range job.tasks {
		sec := t.Chunk.Estimate.Seconds()
		total += sec
		if c.cached(req, job, t.CacheKey) {
			ev.Cached++
		} else {
			left += sec
		}
	}
	ev.EstSeconds = round3(total)
	ev.EstWallSeconds = round3(left / (realtimePerWorker * float64(max(req.Jobs, 1))))
	sttModel := ""
	if req.Verify.Mode != verify.ModeOff {
		sttModel = stt.HTTPModel
	}
	if usd, ok := usage.ListPrices.EstimateSpeech(req.Settings.Model, sttModel, left); ok {
		ev.EstimateUSD = ptr(usd)
	}
	return ev
}

// cached reports whether the take cache holds a take for key. A stored take may still be
// re-verified, which the plan cannot know without the verifier's route.
func (c *CLI) cached(req SayRequest, job sayJob, key string) bool {
	if job.cache == nil || req.Refresh || key == "" {
		return false
	}
	ok, err := job.cache.Store.Peek(key)
	if err != nil {
		c.Logger.Debug("cache entry ignored", "err", err)
	}
	return ok
}

// writePlan prints the dry-run report for people.
func (c *CLI) writePlan(ev planEvent, model string) int {
	price := "unknown (no list price for " + model + ")"
	if ev.EstimateUSD != nil {
		price = fmt.Sprintf("$%.4f (%s)", *ev.EstimateUSD, ev.PriceNote)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "chunks:     %d\n", ev.Chunks)
	fmt.Fprintf(&b, "cached:     %d\n", ev.Cached)
	fmt.Fprintf(&b, "audio:      about %s\n", clock(ev.EstSeconds))
	fmt.Fprintf(&b, "wall time:  about %s at --jobs %d\n", clock(ev.EstWallSeconds), ev.Jobs)
	fmt.Fprintf(&b, "price:      %s\n", price)
	_, err := io.WriteString(c.Stdout, logx.Scrub(b.String()))
	return c.writeResult(err)
}
