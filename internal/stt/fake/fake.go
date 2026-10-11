// Package fake provides ScriptedTranscriber, an stt.Transcriber scripted per chunk ID and
// attempt (FND-06). Takes are identified by the tag that realtime/fake.TaggedPCM writes.
package fake

import (
	"context"
	"sync"

	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
	"github.com/xiaolai/recite/internal/stt"
)

// Outcome is one scripted Transcribe result.
type Outcome struct {
	Transcript stt.Transcript
	Err        error
}

// Text is a successful transcript.
func Text(text string, langs ...string) Outcome {
	t := stt.Transcript{Text: text, Languages: []stt.DetectedLanguage{}}
	for _, l := range langs {
		t.Languages = append(t.Languages, stt.DetectedLanguage{Code: l})
	}
	return Outcome{Transcript: t}
}

// Fail makes Transcribe return err (see realtime/fake.ErrClass for classed errors).
func Fail(err error) Outcome { return Outcome{Err: err} }

// Call records one Transcribe.
type Call struct {
	ChunkID     string // "" when the PCM carries no tag
	TakeAttempt int    // the synthesizer attempt in the tag
	Attempt     int    // 1-based Transcribe count for this chunk ID
	Hint        stt.Hint
	Bytes       int
}

// ScriptedTranscriber implements stt.Transcriber. Safe for concurrent use.
type ScriptedTranscriber struct {
	mu       sync.Mutex
	scripts  map[string][]Outcome
	attempts map[string]int
	calls    []Call

	// Default is used when no scripted outcome remains.
	Default Outcome
}

// NewScriptedTranscriber returns a transcriber whose default is an empty transcript.
func NewScriptedTranscriber() *ScriptedTranscriber {
	return &ScriptedTranscriber{scripts: map[string][]Outcome{}, attempts: map[string]int{}}
}

// On scripts consecutive Transcribe calls for chunkID, starting at attempt 1.
func (s *ScriptedTranscriber) On(chunkID string, outcomes ...Outcome) *ScriptedTranscriber {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scripts[chunkID] = append(s.scripts[chunkID], outcomes...)
	return s
}

// Calls returns every call in call order.
func (s *ScriptedTranscriber) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// Transcribe implements stt.Transcriber.
func (s *ScriptedTranscriber) Transcribe(ctx context.Context, pcm []byte, hint stt.Hint) (stt.Transcript, error) {
	if err := ctx.Err(); err != nil {
		return stt.Transcript{}, err
	}
	id, takeAttempt, _ := rtfake.PCMTag(pcm)
	s.mu.Lock()
	s.attempts[id]++
	n := s.attempts[id]
	s.calls = append(s.calls, Call{ChunkID: id, TakeAttempt: takeAttempt, Attempt: n, Hint: hint, Bytes: len(pcm)})
	o := s.Default
	if sc := s.scripts[id]; n <= len(sc) {
		o = sc[n-1]
	}
	s.mu.Unlock()
	if o.Err != nil {
		return stt.Transcript{}, o.Err
	}
	t := o.Transcript
	if t.Seconds == 0 {
		t.Seconds = (len(pcm) + rtfake.BytesPerSecond - 1) / rtfake.BytesPerSecond
	}
	return t, nil
}

var _ stt.Transcriber = (*ScriptedTranscriber)(nil)
