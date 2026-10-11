//go:build live

package realtime_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/xiaolai/recite/internal/realtime"
)

// accept: SPK-06#4
func TestLiveSixParallelRenders(t *testing.T) {
	s := liveSynth(t)
	spec, err := realtime.DefaultSettings().SessionSpec()
	if err != nil {
		t.Fatal(err)
	}
	p, err := realtime.NewPool(realtime.PoolConfig{Synth: s, Max: 6})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range 6 {
		wg.Go(func() {
			l, err := p.Acquire(context.Background(), spec)
			if err != nil {
				errs[i] = err
				return
			}
			defer l.Release()
			take, err := l.Render(context.Background(), realtime.RenderRequest{ChunkID: fmt.Sprintf("c%04d", i+1), Text: "Six sessions render at once."})
			if err == nil && (take.Status != realtime.StatusCompleted || len(take.PCM) == 0) {
				err = fmt.Errorf("status %q, %d bytes", take.Status, len(take.PCM))
			}
			errs[i] = err
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("render %d: %v", i+1, err)
		}
	}
}
