package realtime

import sr "github.com/xiaolai/sayso/go/realtime"

// The server events (NET-04) are decoded by github.com/xiaolai/sayso/go/realtime; these are its
// types under recite's names.
type (
	Event                  = sr.Event
	SessionCreated         = sr.SessionCreated
	ResponseCreated        = sr.ResponseCreated
	AudioDelta             = sr.AudioDelta
	TranscriptDelta        = sr.TranscriptDelta
	ResponseDone           = sr.ResponseDone
	RateLimit              = sr.RateLimit
	RateLimitsUpdated      = sr.RateLimitsUpdated
	ErrorEvent             = sr.ErrorEvent
	InputCommitted         = sr.InputCommitted
	TranscriptionDelta     = sr.TranscriptionDelta
	TranscriptionCompleted = sr.TranscriptionCompleted
	TranscriptionFailed    = sr.TranscriptionFailed
	Ignored                = sr.Ignored
	Unknown                = sr.Unknown
	// ResponseStatus is response.done.status: "completed" | "failed" | "incomplete" | "cancelled".
	ResponseStatus = sr.ResponseStatus
	// TokenUsage is the response.done usage (the manifest's text_in, text_out and audio_out,
	// design §11.2).
	TokenUsage = sr.TokenUsage
)

// Event types and response statuses.
const (
	TypeSessionCreated         = sr.TypeSessionCreated
	TypeResponseCreated        = sr.TypeResponseCreated
	TypeAudioDelta             = sr.TypeAudioDelta
	TypeTranscriptDelta        = sr.TypeTranscriptDelta
	TypeResponseDone           = sr.TypeResponseDone
	TypeRateLimitsUpdated      = sr.TypeRateLimitsUpdated
	TypeError                  = sr.TypeError
	TypeInputCommitted         = sr.TypeInputCommitted
	TypeTranscriptionDelta     = sr.TypeTranscriptionDelta
	TypeTranscriptionCompleted = sr.TypeTranscriptionCompleted
	TypeTranscriptionFailed    = sr.TypeTranscriptionFailed

	StatusCompleted  = sr.StatusCompleted
	StatusFailed     = sr.StatusFailed
	StatusIncomplete = sr.StatusIncomplete
	StatusCancelled  = sr.StatusCancelled
)

// DecodeEvent decodes one server event by its type; a malformed event or a missing required
// field is a protocol error naming the type only.
func DecodeEvent(data []byte) (Event, error) { return sr.DecodeEvent(data) }
