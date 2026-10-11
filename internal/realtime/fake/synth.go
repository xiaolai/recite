package fake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/xiaolai/recite/internal/realtime"
)

// BytesPerSecond is 24 kHz 16-bit mono PCM, the realtime output format.
const BytesPerSecond = 48000

// ClassError is a scripted failure carrying an error class name (NET-05 classes such as
// "rate-limited" or "transient"); the real classification type arrives with WP-1.2.
type ClassError struct {
	Class string
	Msg   string
}

func (e *ClassError) Error() string { return "fake " + e.Class + ": " + e.Msg }

// ErrClass returns a *ClassError of the given class.
func ErrClass(class string) error { return &ClassError{Class: class, Msg: "scripted failure"} }

// Outcome is one scripted Render result.
type Outcome struct {
	Said     string                  // spoken transcript; "" with Echo = the request text
	Echo     bool                    // Said = request text
	Duration time.Duration           // PCM length
	Status   realtime.ResponseStatus // "" = completed
	Err      error                   // non-nil = Render fails with it
}

// Pass is a completed take that says the request text exactly.
func Pass(d time.Duration) Outcome { return Outcome{Echo: true, Duration: d} }

// Say is a completed take whose transcript is said.
func Say(said string, d time.Duration) Outcome { return Outcome{Said: said, Duration: d} }

// Fail makes Render return err.
func Fail(err error) Outcome { return Outcome{Err: err} }

// RenderCall records one Render.
type RenderCall struct {
	Request realtime.RenderRequest
	Attempt int // 1-based, per chunk ID across all sessions
	Session int // 1-based session number
}

// ScriptedSynthesizer is a realtime.Synthesizer scripted per chunk ID and attempt. Unscripted
// attempts (or attempts past the end of a script) use Default. Safe for concurrent use.
type ScriptedSynthesizer struct {
	mu       sync.Mutex
	scripts  map[string][]Outcome
	attempts map[string]int
	calls    []RenderCall
	specs    []realtime.SessionSpec
	openErrs []error

	// Default is used when no scripted outcome remains. Zero value: Pass(1s).
	Default Outcome
}

// NewScriptedSynthesizer returns a synthesizer whose default is Pass(1s).
func NewScriptedSynthesizer() *ScriptedSynthesizer {
	return &ScriptedSynthesizer{
		scripts: map[string][]Outcome{}, attempts: map[string]int{},
		Default: Pass(time.Second),
	}
}

// On scripts consecutive attempts for chunkID, starting at attempt 1.
func (s *ScriptedSynthesizer) On(chunkID string, outcomes ...Outcome) *ScriptedSynthesizer {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scripts[chunkID] = append(s.scripts[chunkID], outcomes...)
	return s
}

// FailOpen scripts consecutive Open failures.
func (s *ScriptedSynthesizer) FailOpen(errs ...error) *ScriptedSynthesizer {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openErrs = append(s.openErrs, errs...)
	return s
}

// Calls returns every Render call in call order.
func (s *ScriptedSynthesizer) Calls() []RenderCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RenderCall(nil), s.calls...)
}

// Attempts returns how many times chunkID was rendered.
func (s *ScriptedSynthesizer) Attempts(chunkID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts[chunkID]
}

// Opens returns the spec of every Open call, including failed ones.
func (s *ScriptedSynthesizer) Opens() []realtime.SessionSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]realtime.SessionSpec(nil), s.specs...)
}

// Open implements realtime.Synthesizer.
func (s *ScriptedSynthesizer) Open(ctx context.Context, spec realtime.SessionSpec) (realtime.SpeechSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.specs = append(s.specs, spec)
	if len(s.openErrs) > 0 {
		err := s.openErrs[0]
		s.openErrs = s.openErrs[1:]
		return nil, err
	}
	return &scriptedSession{s: s, n: len(s.specs)}, nil
}

type scriptedSession struct {
	s       *ScriptedSynthesizer
	n       int
	mu      sync.Mutex
	closed  bool
	renders int
}

// ErrSessionClosed is returned by Render after Close.
var ErrSessionClosed = errors.New("fake: session closed")

func (ss *scriptedSession) Render(ctx context.Context, req realtime.RenderRequest) (realtime.Take, error) {
	if err := ctx.Err(); err != nil {
		return realtime.Take{}, err
	}
	ss.mu.Lock()
	closed := ss.closed
	ss.renders++
	ss.mu.Unlock()
	if closed {
		return realtime.Take{}, ErrSessionClosed
	}
	s := ss.s
	s.mu.Lock()
	s.attempts[req.ChunkID]++
	attempt := s.attempts[req.ChunkID]
	s.calls = append(s.calls, RenderCall{Request: req, Attempt: attempt, Session: ss.n})
	o := s.Default
	if sc := s.scripts[req.ChunkID]; attempt <= len(sc) {
		o = sc[attempt-1]
	}
	s.mu.Unlock()
	if o.Err != nil {
		return realtime.Take{}, o.Err
	}
	said := o.Said
	if o.Echo {
		said = req.Text
	}
	status := o.Status
	if status == "" {
		status = realtime.StatusCompleted
	}
	return realtime.Take{
		PCM:    TaggedPCM(req.ChunkID, attempt, o.Duration),
		Said:   said,
		Status: status,
		Wall:   o.Duration,
	}, nil
}

// Age reports how many renders the session has served, in seconds, so tests can drive
// session-rotation logic without a clock.
func (ss *scriptedSession) Age() time.Duration {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return time.Duration(ss.renders) * time.Second
}

func (ss *scriptedSession) Close() error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.closed = true
	return nil
}

// tagMagic starts every PCM buffer made by TaggedPCM.
var tagMagic = []byte("FAKEPCM:")

// TaggedPCM returns d of PCM whose leading bytes identify chunkID and attempt, so a scripted
// transcriber can tell takes apart. The rest is a ±4096 square wave (−18 dBFS), so silence
// trimming keeps the whole take as it would keep speech.
func TaggedPCM(chunkID string, attempt int, d time.Duration) []byte {
	n := int(d.Seconds()*BytesPerSecond) &^ 1
	tag := fmt.Appendf(append([]byte(nil), tagMagic...), "%s#%d;", chunkID, attempt)
	if len(tag)%2 == 1 {
		tag = append(tag, ';')
	}
	if n < len(tag) {
		n = len(tag)
	}
	pcm := make([]byte, n)
	for i := 0; i+1 < n; i += 2 {
		pcm[i+1] = 0x10 // +4096 little-endian
		if (i/2)%2 == 1 {
			pcm[i+1] = 0xF0 // −4096
		}
	}
	copy(pcm, tag)
	return pcm
}

// PCMTag reads the tag written by TaggedPCM. The tag is searched anywhere in the buffer, not
// only at the start, because the pipeline pads takes with silence before transcription.
func PCMTag(pcm []byte) (chunkID string, attempt int, ok bool) {
	at := bytes.Index(pcm, tagMagic)
	if at < 0 {
		return "", 0, false
	}
	rest := pcm[at+len(tagMagic):]
	end := bytes.IndexByte(rest, ';')
	if end < 0 {
		return "", 0, false
	}
	body := string(rest[:end])
	hash := bytes.LastIndexByte(rest[:end], '#')
	if hash < 0 {
		return "", 0, false
	}
	a, err := strconv.Atoi(body[hash+1:])
	if err != nil {
		return "", 0, false
	}
	return body[:hash], a, true
}
