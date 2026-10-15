package realtime

import (
	"context"
	"fmt"
	"net/http"
	"time"

	sr "github.com/xiaolai/sayso/go/realtime"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
)

// Session-body constants (design §6.1).
const (
	// SecretTTL is the lifetime asked for a minted client secret.
	SecretTTL = 600 * time.Second
	// SampleRate is the PCM rate of realtime audio, in and out.
	SampleRate = 24000
)

// Efforts lists the reasoning efforts the session accepts.
var Efforts = []Effort{"minimal", "low", "medium", "high", "xhigh"}

// The wire shapes of the session objects. Every field is typed so a golden test pins the exact
// JSON; nothing is built from maps. The mint body around them is sayso's (sr.MintBody).
type speechSession struct {
	Type             string      `json:"type"`
	Model            string      `json:"model"`
	Instructions     string      `json:"instructions"`
	Tools            []struct{}  `json:"tools"`
	ToolChoice       string      `json:"tool_choice"`
	OutputModalities []string    `json:"output_modalities"`
	Reasoning        reasoning   `json:"reasoning"`
	Audio            speechAudio `json:"audio"`
}

type reasoning struct {
	Effort Effort `json:"effort"`
}

type speechAudio struct {
	Output audioOutput `json:"output"`
}

type audioOutput struct {
	Format audioFormat `json:"format"`
	Voice  string      `json:"voice"`
	Speed  float64     `json:"speed"`
}

type audioFormat struct {
	Type string `json:"type"`
	Rate int    `json:"rate"`
}

type transcriptionSession struct {
	Type  string             `json:"type"`
	Audio transcriptionAudio `json:"audio"`
}

type transcriptionAudio struct {
	Input transcriptionInput `json:"input"`
}

type transcriptionInput struct {
	Format        audioFormat        `json:"format"`
	Transcription transcriptionModel `json:"transcription"`
	// TurnDetection is always null: recite commits the audio buffer itself.
	TurnDetection *struct{} `json:"turn_detection"`
}

type transcriptionModel struct {
	Model string `json:"model"`
}

func pcm() audioFormat { return audioFormat{Type: "audio/pcm", Rate: SampleRate} }

// SpeechSessionBody is the mint body of a speech session (design §6.1): no
// tools, audio output only, PCM 24 kHz. User-facing validation of the values
// is SPK-02..05; a bad value here is a programming error.
func SpeechSessionBody(spec SessionSpec) ([]byte, error) {
	session, err := speechSessionFor(spec)
	if err != nil {
		return nil, err
	}
	return sr.MintBody(session, SecretTTL)
}

func speechSessionFor(spec SessionSpec) (speechSession, error) {
	if spec.Model == "" || spec.Voice == "" || spec.Speed <= 0 {
		return speechSession{}, fmt.Errorf("realtime: incomplete speech session spec")
	}
	if !validEffort(spec.Effort) {
		return speechSession{}, fmt.Errorf("realtime: unknown effort %q", spec.Effort)
	}
	return speechSession{
		Type:             "realtime",
		Model:            spec.Model,
		Instructions:     spec.Instructions,
		Tools:            []struct{}{},
		ToolChoice:       "none",
		OutputModalities: []string{"audio"},
		Reasoning:        reasoning{Effort: spec.Effort},
		Audio:            speechAudio{Output: audioOutput{Format: pcm(), Voice: spec.Voice, Speed: spec.Speed}},
	}, nil
}

// TranscriptionSessionBody is the mint body of a realtime transcription
// session, the VER-02 fallback verifier.
func TranscriptionSessionBody(model string) ([]byte, error) {
	session, err := transcriptionSessionFor(model)
	if err != nil {
		return nil, err
	}
	return sr.MintBody(session, SecretTTL)
}

func transcriptionSessionFor(model string) (transcriptionSession, error) {
	if model == "" {
		return transcriptionSession{}, fmt.Errorf("realtime: empty transcription model")
	}
	return transcriptionSession{
		Type: "transcription",
		Audio: transcriptionAudio{Input: transcriptionInput{
			Format:        pcm(),
			Transcription: transcriptionModel{Model: model},
		}},
	}, nil
}

func validEffort(e Effort) bool {
	for _, v := range Efforts {
		if e == v {
			return true
		}
	}
	return false
}

// Credentials yields the bearer and refreshes it after a refusal; *auth.Credentials implements it
// (AUTH-04).
type Credentials = sr.Credentials

// Minter mints ephemeral client secrets (NET-02) through github.com/xiaolai/sayso/go/realtime,
// with recite's session bodies, timeout and secret lifetime.
type Minter struct {
	Client  *http.Client // the production client from openai.NewClient
	BaseURL string       // openai.APIBase; tests use a reserved test origin
	Creds   Credentials
}

// MintSpeech mints a secret for a speech session.
func (m *Minter) MintSpeech(ctx context.Context, spec SessionSpec) (auth.ClientSecret, error) {
	session, err := speechSessionFor(spec)
	if err != nil {
		return auth.ClientSecret{}, err
	}
	return m.mint(ctx, session, openai.RouteSpeechMint)
}

// MintTranscription mints a secret for a realtime transcription session.
func (m *Minter) MintTranscription(ctx context.Context, model string) (auth.ClientSecret, error) {
	session, err := transcriptionSessionFor(model)
	if err != nil {
		return auth.ClientSecret{}, err
	}
	return m.mint(ctx, session, openai.RouteRealtimeSTT)
}

// MsgSignInRefused is recite's message when the sign-in is refused and a refresh did not renew it.
const MsgSignInRefused = "the ChatGPT sign-in was refused and could not be renewed; run any `codex` command to refresh it, or run `codex login`"

// mint refreshes once through Creds on auth-expired and retries once (AUTH-04).
func (m *Minter) mint(ctx context.Context, session any, route openai.Route) (auth.ClientSecret, error) {
	sm := &sr.Minter{Client: m.Client, BaseURL: m.BaseURL, Creds: m.Creds, Timeout: openai.MintTimeout,
		Lifetime: SecretTTL, Route: route}
	sec, err := sm.Secret(ctx, session)
	if openai.Classify(err) == openai.ClassAuthExpired {
		return auth.ClientSecret{}, &openai.Error{Class: openai.ClassAuthExpired, Message: MsgSignInRefused, Err: err}
	}
	return sec, err
}
