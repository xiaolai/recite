//go:build qualify

package spoken_test

import (
	"sync"
	"testing"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/verify"
)

// deliveredAudio keeps the audio of every chunk as the pipeline delivers it. The scheduler
// drops a result's PCM once Deliver has it, so the results of pipeline.Run carry no audio.
type deliveredAudio struct {
	mu  sync.Mutex
	pcm map[string]audio.PCM
}

func newDeliveredAudio() *deliveredAudio { return &deliveredAudio{pcm: map[string]audio.PCM{}} }

// Deliver is the pipeline.Config.Deliver that keeps the audio.
func (d *deliveredAudio) Deliver(_ int, r verify.ChunkResult) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pcm[r.ChunkID] = r.PCM
	return nil
}

// Of is the kept audio of r's chunk. A verified chunk always has audio, so one without fails
// the test rather than being reported as an empty take.
func (d *deliveredAudio) Of(t *testing.T, r verify.ChunkResult) audio.PCM {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.pcm[r.ChunkID]
	if len(p) == 0 && r.State == verify.StateVerified {
		t.Fatalf("chunk %s verified but no audio was delivered", r.ChunkID)
	}
	return p
}
