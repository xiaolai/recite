package stt

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
)

const (
	// RealtimeModel is the realtime transcription model of the VER-02 fallback.
	RealtimeModel = "gpt-live-transcribe"
	// RealtimePace is how much faster than real time audio is streamed.
	RealtimePace = 8
	// FrameDuration is the audio carried by one input_audio_buffer.append.
	FrameDuration = 200 * time.Millisecond
)

// Realtime transcribes takes over one realtime transcription session (VER-02): turn
// detection is off, so each take is one input_audio_buffer.commit, and its transcript is the
// input_audio_transcription.completed event of the item that commit created. The session is
// opened on first use and reused across takes; a broken session is dropped and the next take
// opens a new one. Takes are serialised: one session carries one commit at a time.
//
// The session's mint body is fixed, so Hint is not sent, and the realtime events carry no
// language detection: Languages is always empty.
type Realtime struct {
	Minter  *realtime.Minter
	Client  *http.Client // the production client
	BaseURL string       // openai.APIBase; tests use a reserved test origin
	Logger  *slog.Logger // nil discards

	// Sleep paces the sender; nil means a real timer. It returns early when ctx ends.
	Sleep func(ctx context.Context, d time.Duration)

	mu   sync.Mutex
	sess *realtime.Session
}

var _ Transcriber = (*Realtime)(nil)

// Close ends the session, if one is open.
func (r *Realtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sess == nil {
		return nil
	}
	err := r.sess.Close()
	r.sess = nil
	return err
}

// Transcribe implements Transcriber.
func (r *Realtime) Transcribe(ctx context.Context, pcm []byte, _ Hint) (Transcript, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	padded := Pad(pcm)
	ctx, cancel := context.WithTimeout(ctx, openai.STTTimeout(audio.PCM(padded).Duration()))
	defer cancel()
	if r.sess == nil {
		s, err := r.open(ctx)
		if err != nil {
			return Transcript{}, err
		}
		r.sess = s
	}
	t, err := r.take(ctx, padded)
	if err != nil {
		// The session's state is unknown after a failed take (a commit may still be answered):
		// never reuse it.
		_ = r.sess.Close()
		r.sess = nil
		return Transcript{}, err
	}
	t.Seconds = int((audio.PCM(padded).Duration() + time.Second - 1) / time.Second)
	return t, nil
}

func (r *Realtime) open(ctx context.Context) (*realtime.Session, error) {
	if r.Minter == nil || r.Client == nil || r.BaseURL == "" {
		return nil, errors.New("stt: Realtime needs a Minter, a Client and a BaseURL")
	}
	secret, err := r.Minter.MintTranscription(ctx, RealtimeModel)
	if err != nil {
		return nil, err
	}
	return realtime.Dial(ctx, realtime.DialConfig{
		Client: r.Client,
		URL:    strings.TrimSuffix(r.BaseURL, "/") + "/v1/realtime?intent=transcription",
		Secret: secret,
		Route:  openai.RouteRealtimeSTT,
		Logger: r.Logger,
	})
}

type appendEvent struct {
	Type  string `json:"type"`
	Audio string `json:"audio"`
}

type commitEvent struct {
	Type string `json:"type"`
}

// take streams pcm from a sender goroutine and waits for the transcript of its commit. With
// turn detection off only a commit creates an item, and a failed take drops the session, so
// the first input_audio_buffer.committed seen during a take is this take's.
func (r *Realtime) take(ctx context.Context, pcm []byte) (Transcript, error) {
	tctx, fail := context.WithCancelCause(ctx)
	var wg sync.WaitGroup
	defer func() {
		fail(nil)
		wg.Wait() // the sender never outlives the take
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := r.send(tctx, pcm); err != nil {
			fail(err) // unblocks Next below
		}
	}()
	itemID := ""
	for {
		ev, err := r.sess.Next(tctx)
		if err != nil {
			if cause := context.Cause(tctx); ev == nil && cause != nil && ctx.Err() == nil {
				return Transcript{}, cause // the sender failed
			}
			return Transcript{}, err
		}
		switch e := ev.(type) {
		case realtime.InputCommitted:
			if itemID == "" {
				itemID = e.ItemID
			}
		case realtime.TranscriptionCompleted:
			if itemID != "" && e.ItemID == itemID {
				return Transcript{Text: e.Transcript, Languages: []DetectedLanguage{}}, nil
			}
		case realtime.TranscriptionFailed:
			if itemID != "" && e.ItemID == itemID {
				return Transcript{}, e.Err
			}
		case realtime.ErrorEvent:
			return Transcript{}, e.Err
		}
		// session.created and transcription deltas carry nothing the take needs.
	}
}

// send streams pcm in FrameDuration frames at RealtimePace × real time, then commits.
func (r *Realtime) send(ctx context.Context, pcm []byte) error {
	frame := int(int64(audio.BytesPerSecond) * int64(FrameDuration) / int64(time.Second))
	gap := FrameDuration / RealtimePace
	for off := 0; off < len(pcm); off += frame {
		if off > 0 {
			r.sleep(ctx, gap)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(off+frame, len(pcm))
		ev := appendEvent{Type: "input_audio_buffer.append", Audio: base64.StdEncoding.EncodeToString(pcm[off:end])}
		if err := r.sess.Send(ctx, ev); err != nil {
			return fmt.Errorf("stt: stream audio: %w", err)
		}
	}
	if err := r.sess.Send(ctx, commitEvent{Type: "input_audio_buffer.commit"}); err != nil {
		return fmt.Errorf("stt: commit audio: %w", err)
	}
	return nil
}

func (r *Realtime) sleep(ctx context.Context, d time.Duration) {
	if r.Sleep != nil {
		r.Sleep(ctx, d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
