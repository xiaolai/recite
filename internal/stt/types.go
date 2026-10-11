package stt

import "context"

// Transcriber turns PCM into text for verification (design §4).
type Transcriber interface {
	Transcribe(ctx context.Context, pcm []byte, hint Hint) (Transcript, error)
}

// Hint narrows transcription. Languages empty = no hint (TXT-04).
type Hint struct {
	Languages []string
	Keywords  []string
}

// Transcript is one transcription result.
type Transcript struct {
	Text      string
	Languages []DetectedLanguage
	Tokens    []TokenLogprob
	Seconds   int
}

// DetectedLanguage is one detected language; the API returns objects and the list may be empty.
type DetectedLanguage struct {
	Code string `json:"code"`
}

// TokenLogprob is one token log-probability, as include[]=logprobs returns it.
type TokenLogprob struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
	Bytes   []int   `json:"bytes"`
}
