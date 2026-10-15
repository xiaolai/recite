package realtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/xiaolai/recite/internal/openai"
)

// Session recycling limits (NET-07).
const (
	DefaultSessionMaxAge = 50 * time.Minute
	DefaultSessionChunks = 50
)

// PoolKey is the pool key of a session spec: model, voice and speed (SPK-06). Effort and
// instructions are not part of it; both are sent per response. It matches pipeline.SessionKey.
func PoolKey(spec SessionSpec) string {
	return spec.Model + "|" + spec.Voice + "|" + strconv.FormatFloat(spec.Speed, 'g', -1, 64)
}

// PoolConfig configures a Pool.
type PoolConfig struct {
	Synth Synthesizer
	Max   int // --jobs: sessions open at once, at most

	MaxAge    time.Duration    // recycle at this age; 0 means DefaultSessionMaxAge
	MaxChunks int              // recycle after this many renders (--session-chunks); 0 means DefaultSessionChunks
	Now       func() time.Time // nil means time.Now
	Logger    *slog.Logger     // nil discards
}

// Pool hands out speech sessions keyed by model, voice and speed (SPK-06), up to Max open at
// once, and recycles them (NET-07): a session is closed instead of reused once it reaches
// MaxAge or MaxChunks renders, or after a server, network or protocol error. Ages are checked
// only between renders, so recycling never fails an in-flight chunk. Safe for concurrent use.
type Pool struct {
	cfg PoolConfig

	mu       sync.Mutex
	sessions []*pooled // open or opening
	tick     uint64    // LRU clock
	closed   bool
}

type pooled struct {
	sess    SpeechSession // nil while opening
	key     string
	opened  time.Time
	renders int
	busy    bool
	used    uint64
}

// ErrPoolExhausted means every session is busy and the pool is full: more workers borrow
// sessions than the pool allows, a programming error.
var ErrPoolExhausted = errors.New("realtime: session pool exhausted (more workers than --jobs)")

// NewPool returns an empty pool.
func NewPool(cfg PoolConfig) (*Pool, error) {
	if cfg.Synth == nil || cfg.Max < 1 {
		return nil, fmt.Errorf("realtime: pool needs a Synthesizer and Max ≥ 1 (got %d)", cfg.Max)
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = DefaultSessionMaxAge
	}
	if cfg.MaxChunks <= 0 {
		cfg.MaxChunks = DefaultSessionChunks
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Pool{cfg: cfg}, nil
}

// Lease is one borrowed session. Render on it, then Release it exactly once.
type Lease struct {
	p       *Pool
	s       *pooled
	discard bool
}

// Acquire borrows a session for spec: an idle one with the same key, else a new one while the
// pool has room, else it closes the least-recently-used idle session of another key and opens
// a replacement. It never waits for a session to become idle.
func (p *Pool) Acquire(ctx context.Context, spec SessionSpec) (*Lease, error) {
	key := PoolKey(spec)
	var victims []SpeechSession
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errors.New("realtime: pool closed")
	}
	var slot *pooled
	for _, s := range p.sessions {
		if !s.busy && s.key == key {
			if p.expired(s) {
				continue
			}
			slot = s
			break
		}
	}
	// Expired idle sessions are closed whatever their key.
	keep := p.sessions[:0]
	for _, s := range p.sessions {
		if !s.busy && s != slot && p.expired(s) {
			victims = append(victims, s.sess)
			continue
		}
		keep = append(keep, s)
	}
	p.sessions = keep
	if slot == nil && len(p.sessions) >= p.cfg.Max {
		lru := -1
		for i, s := range p.sessions {
			if !s.busy && (lru < 0 || s.used < p.sessions[lru].used) {
				lru = i
			}
		}
		if lru < 0 {
			p.mu.Unlock()
			closeAll(victims, p.cfg.Logger)
			return nil, ErrPoolExhausted
		}
		victims = append(victims, p.sessions[lru].sess)
		p.sessions = append(p.sessions[:lru], p.sessions[lru+1:]...)
	}
	opening := slot == nil
	if opening {
		slot = &pooled{key: key}
		p.sessions = append(p.sessions, slot)
	}
	slot.busy = true
	p.tick++
	slot.used = p.tick
	p.mu.Unlock()

	closeAll(victims, p.cfg.Logger)
	if !opening {
		return &Lease{p: p, s: slot}, nil
	}
	sess, err := p.cfg.Synth.Open(ctx, spec)
	if err != nil {
		p.remove(slot)
		return nil, err
	}
	p.mu.Lock()
	slot.sess, slot.opened = sess, p.cfg.Now()
	p.mu.Unlock()
	p.cfg.Logger.Debug("speech session opened", "key", key)
	return &Lease{p: p, s: slot}, nil
}

// expired reports a session due for recycling. Callers hold p.mu.
func (p *Pool) expired(s *pooled) bool {
	return s.renders >= p.cfg.MaxChunks || p.cfg.Now().Sub(s.opened) >= p.cfg.MaxAge
}

func (p *Pool) remove(slot *pooled) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, s := range p.sessions {
		if s == slot {
			p.sessions = append(p.sessions[:i], p.sessions[i+1:]...)
			return
		}
	}
}

func closeAll(ss []SpeechSession, logger *slog.Logger) {
	for _, s := range ss {
		if err := s.Close(); err != nil {
			logger.Debug("close speech session", "err", err)
		}
	}
}

// Render renders req on the leased session. A server, network or protocol failure, or a
// cancelled render, marks the session for closing on Release.
func (l *Lease) Render(ctx context.Context, req RenderRequest) (Take, error) {
	take, err := l.s.sess.Render(ctx, req)
	l.p.mu.Lock()
	l.s.renders++
	l.p.mu.Unlock()
	if err != nil {
		var oe *openai.Error
		switch fatal := errors.As(err, &oe) && oe.Fatal; {
		case fatal:
			l.discard = true // session_expired, session_not_found and the like end it whatever the class
		case openai.Classify(err) == openai.ClassRateLimited, openai.Classify(err) == openai.ClassInvalidRequest:
			// The session itself is fine.
		default:
			l.discard = true
		}
	}
	return take, err
}

// Release returns the session to the pool, or closes it when it is due for recycling or
// broken.
func (l *Lease) Release() {
	p := l.p
	p.mu.Lock()
	l.s.busy = false
	recycle := l.discard || p.closed || p.expired(l.s)
	p.mu.Unlock()
	if !recycle {
		return
	}
	p.remove(l.s)
	closeAll([]SpeechSession{l.s.sess}, p.cfg.Logger)
}

// Open returns how many sessions are open or opening.
func (p *Pool) Open() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sessions)
}

// Close closes every idle session and makes busy ones close on Release.
func (p *Pool) Close() error {
	p.mu.Lock()
	p.closed = true
	var idle []SpeechSession
	keep := p.sessions[:0]
	for _, s := range p.sessions {
		if !s.busy && s.sess != nil {
			idle = append(idle, s.sess)
			continue
		}
		keep = append(keep, s)
	}
	p.sessions = keep
	p.mu.Unlock()
	closeAll(idle, p.cfg.Logger)
	return nil
}
