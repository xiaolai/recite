package pipeline

import (
	"context"
	"os"
	"sync"
	"time"
)

// Interrupt is the two-stage cancellation of RUN-03: the first signal closes Stop (no new
// work; in-flight work may finish), and the context is cancelled after the drain timeout or
// at the second signal, whichever comes first.
type Interrupt struct {
	stop chan struct{}
	once sync.Once
	done chan struct{}
	quit chan struct{}
}

// WatchSignals starts watching sigs. after is the drain timer (nil means time.After). Call
// the returned release when the run is over: it ends the watcher and cancels the context.
func WatchSignals(parent context.Context, sigs <-chan os.Signal, drain time.Duration, after func(time.Duration) <-chan time.Time) (*Interrupt, context.Context, func()) {
	if after == nil {
		after = time.After
	}
	if drain <= 0 {
		drain = DrainTimeout
	}
	ctx, cancel := context.WithCancel(parent)
	in := &Interrupt{stop: make(chan struct{}), done: make(chan struct{}), quit: make(chan struct{})}
	go func() {
		defer close(in.done)
		var timer <-chan time.Time
		for {
			select {
			case <-sigs:
				if in.Interrupted() {
					cancel() // the second signal aborts at once
					continue
				}
				in.once.Do(func() { close(in.stop) })
				timer = after(drain)
			case <-timer:
				timer = nil
				cancel()
			case <-in.quit:
				return
			}
		}
	}()
	release := func() {
		close(in.quit)
		<-in.done
		cancel()
	}
	return in, ctx, release
}

// Stop is closed by the first signal.
func (in *Interrupt) Stop() <-chan struct{} { return in.stop }

// Interrupted reports whether a signal has arrived.
func (in *Interrupt) Interrupted() bool {
	select {
	case <-in.stop:
		return true
	default:
		return false
	}
}
