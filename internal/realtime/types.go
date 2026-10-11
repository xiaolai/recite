package realtime

import (
	"context"
	"time"
)

// Synthesizer opens speech sessions (design §4).
type Synthesizer interface {
	Open(ctx context.Context, spec SessionSpec) (SpeechSession, error)
}

// SpeechSession renders one chunk per out-of-band response.
type SpeechSession interface {
	Render(ctx context.Context, c RenderRequest) (Take, error) // one out-of-band response
	Age() time.Duration
	Close() error
}

// SessionSpec configures a speech session.
type SessionSpec struct {
	Model, Voice string
	Speed        float64
	Instructions string
	Effort       Effort
}

// Effort is the reasoning effort: "minimal" | "low" | "medium" | "high" | "xhigh".
type Effort string

// RenderRequest asks for one chunk. Effort is the effective effort; it is sent as an override
// only when it differs from the session's.
type RenderRequest struct {
	ChunkID, Text, Style string
	Effort               Effort
}

// Take is one rendered response.
type Take struct {
	PCM              []byte
	Said             string
	Status           ResponseStatus
	Usage            TokenUsage
	FirstAudio, Wall time.Duration
}

// ResponseStatus is response.done.status: "completed" | "failed" | "incomplete" | "cancelled".
type ResponseStatus string

// Response statuses reported by response.done.
const (
	StatusCompleted  ResponseStatus = "completed"
	StatusFailed     ResponseStatus = "failed"
	StatusIncomplete ResponseStatus = "incomplete"
	StatusCancelled  ResponseStatus = "cancelled"
)

// TokenUsage is the response.done usage. Design §4 names the type without fields; these are the
// minimal fields the fakes need, and the realtime client WP may extend them.
type TokenUsage struct {
	InputTokens, OutputTokens int
}
