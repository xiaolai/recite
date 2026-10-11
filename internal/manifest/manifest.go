package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/buildinfo"
	"github.com/xiaolai/recite/internal/logx"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/verify"
)

// Schema identifies manifest v1 (design §11.2; docs/schema/manifest.schema.json).
const Schema = "recite/manifest@1"

// Status says how far the run got.
type Status string

// Run statuses.
const (
	StatusComplete    Status = "complete"    // the output was written
	StatusNoOutput    Status = "no-output"   // the verdicts allowed no output (VER-09)
	StatusInterrupted Status = "interrupted" // SIGINT: partial state, the cache keeps the takes
	StatusFailed      Status = "failed"      // a render or transcription failure ended the run
)

// Manifest is the durable record of one run. It holds no secret and no account identifier:
// only settings, hashes, verdicts and counts, and the scrubbed cause of a verifier fallback.
type Manifest struct {
	Schema    string    `json:"schema"`
	Tool      Tool      `json:"tool"`
	CreatedAt string    `json:"created_at"`
	Status    Status    `json:"status"`
	Input     Input     `json:"input"`
	Settings  Settings  `json:"settings"`
	Verifier  Verifier  `json:"verifier"`
	Output    *Output   `json:"output"`
	Summary   Summary   `json:"summary"`
	Chunks    []Chunk   `json:"chunks"`
	Chapters  []Chapter `json:"chapters"`

	UsageEstimate float64 `json:"usage_estimate_usd_list_price"`
	UsageNote     string  `json:"usage_estimate_note"`
}

// Chapter is one section of the input (a Markdown heading) on the assembly timeline (OUT-04):
// it runs from its first chunk's start to the next chapter's start, the last one to the end
// of the audio. The times are null when the run did not assemble that far. Path is the
// section's file under --split-sections, else null.
type Chapter struct {
	Title      string   `json:"title"`
	FirstChunk string   `json:"first_chunk"`
	StartS     *float64 `json:"start_s"`
	EndS       *float64 `json:"end_s"`
	Path       *string  `json:"path"`
}

// Section is a section as Build reads it: its title, the index of its first chunk, and its
// file ("" when the run wrote none of its own).
type Section struct {
	Title string
	First int
	Path  string
}

// Tool is the build that wrote the manifest.
type Tool struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// Input describes the text read.
type Input struct {
	Source string `json:"source"` // the file, "-" for stdin, "args" for command-line text
	Format string `json:"format"`
	SHA256 string `json:"sha256"`
	Chars  int    `json:"chars"`
}

// Settings are the render and verification settings.
type Settings struct {
	Model               string         `json:"model"`
	Voice               string         `json:"voice"`
	Speed               float64        `json:"speed"`
	Effort              string         `json:"effort"`
	InstructionsVersion int            `json:"instructions_version"`
	Emotion             Emotion        `json:"emotion"`
	Style               *string        `json:"style"` // null without a delivery style
	Verify              VerifySettings `json:"verify"`
}

// Emotion is the delivery preset and the version of its sentences (SPK-07).
type Emotion struct {
	Preset  string `json:"preset"`
	Version int    `json:"version"`
}

// VerifySettings are the verification thresholds and limits.
type VerifySettings struct {
	Mode                 string  `json:"mode"`
	Accept               float64 `json:"accept"`
	Gray                 float64 `json:"gray"` // the lower bound of the gray zone
	MaxAttempts          int     `json:"max_attempts"`
	RequestExtraAttempts int     `json:"request_extra_attempts"`
}

// Verifier is the verifier route the run ended on, and the VER-02 fallback if it happened.
type Verifier struct {
	Route    string    `json:"route"` // "http", "realtime", or "none" under --verify off
	STTModel *string   `json:"stt_model"`
	Fallback *Fallback `json:"fallback"`
}

// Fallback records the switch from HTTP transcription to the realtime transcription session.
type Fallback struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Cause string `json:"cause"`
}

// Output describes the written audio.
type Output struct {
	Path       string  `json:"path"`
	Format     string  `json:"format"`
	DurationS  float64 `json:"duration_s"`
	SampleRate int     `json:"sample_rate"`
	Loudness   string  `json:"loudness"`
}

// Summary counts the run.
type Summary struct {
	Chunks            int            `json:"chunks"`
	Verified          int            `json:"verified"`
	Unverified        int            `json:"unverified"`
	NoEligible        int            `json:"no_eligible"`
	Pending           int            `json:"pending"`
	Cached            int            `json:"cached"`
	Findings          map[string]int `json:"findings"`
	RequestLikeChunks int            `json:"request_like_chunks"`
	Renders           int            `json:"renders"`
	STTCalls          int            `json:"stt_calls"`
}

// Chunk is one chunk's record. The timeline fields are null for a chunk the run did not
// finish.
type Chunk struct {
	ID           string          `json:"id"`
	Section      *string         `json:"section"`
	TextSHA256   string          `json:"text_sha256"`
	SpokenSHA256 string          `json:"spoken_sha256"`
	Chars        int             `json:"chars"`
	Lang         string          `json:"lang"`
	Findings     []Finding       `json:"findings"`
	RequestLike  bool            `json:"request_like"`
	StartS       *float64        `json:"start_s"`
	EndS         *float64        `json:"end_s"`
	GapAfterS    *float64        `json:"gap_after_s"`
	GapType      string          `json:"gap_type"`
	CacheKey     string          `json:"cache_key"`
	Cached       bool            `json:"cached"`
	State        string          `json:"state"` // verified, unverified, no-eligible or pending
	Verified     bool            `json:"verified"`
	Attempts     int             `json:"attempts"`
	FinalAttempt int             `json:"final_attempt"` // 0 when no take is kept
	Score        float64         `json:"score"`
	STTCalls     int             `json:"stt_calls"`
	AttemptLog   []AttemptRecord `json:"attempt_log"`
	Usage        ChunkUsage      `json:"usage"`
}

// Finding is a guard or spoken-form finding of the chunk.
type Finding struct {
	Code   string `json:"code"`
	At     string `json:"at"`
	Action string `json:"action"`
}

// AttemptRecord is the VER-08 record of one take: its status, whether it passed the pre-STT
// guards, each transcription's score, and why it was rejected.
type AttemptRecord struct {
	N         int        `json:"n"`
	Status    string     `json:"status"`
	Eligible  bool       `json:"eligible"`
	STTScores []float64  `json:"stt_scores"`
	Score     float64    `json:"score"`
	Rejection *Rejection `json:"rejection"`
}

// Rejection is why a take was rejected.
type Rejection struct {
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// ChunkUsage is the usage the chunk cost this run (zero for a cached chunk).
type ChunkUsage struct {
	TextIn     int `json:"text_in"`
	TextOut    int `json:"text_out"`
	AudioOut   int `json:"audio_out"`
	STTSeconds int `json:"stt_seconds"`
}

// Run is everything Build reads.
type Run struct {
	CreatedAt time.Time
	Status    Status
	Input     Input
	Settings  Settings
	Verifier  Verifier
	Output    *Output

	Plan    pipeline.Plan
	Tasks   []pipeline.Task      // in plan order
	Results []verify.ChunkResult // the delivered prefix, in plan order
	Spans   []audio.Span         // the assembly's place of each delivered chunk
	Gaps    []int                // the silence after each chunk, in samples
	// Sections are the input's sections in order, each starting at a later chunk.
	Sections []Section

	EstimateUSD float64 // the list-price estimate of every render and STT call of the run
	PriceNote   string
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// SHA256 is the hex SHA-256 of s, as the manifest records hashes.
func SHA256(s string) string { return sha(s) }

func seconds(samples int64) *float64 {
	s := float64(samples) / audio.SampleRate
	return &s
}

// Build assembles the manifest of r.
func Build(r Run) (Manifest, error) {
	if len(r.Tasks) != len(r.Plan.Chunks) || len(r.Gaps) != len(r.Plan.Chunks) {
		return Manifest{}, fmt.Errorf("manifest: %d chunks, %d tasks, %d gaps", len(r.Plan.Chunks), len(r.Tasks), len(r.Gaps))
	}
	if len(r.Results) > len(r.Plan.Chunks) || len(r.Spans) < len(r.Results) && r.Status == StatusComplete {
		return Manifest{}, fmt.Errorf("manifest: %d results, %d spans for %d chunks", len(r.Results), len(r.Spans), len(r.Plan.Chunks))
	}
	bi := buildinfo.Get()
	m := Manifest{
		Schema: Schema, Tool: Tool{Version: bi.Version, Commit: bi.Commit},
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339), Status: r.Status,
		Input: r.Input, Settings: r.Settings, Verifier: r.Verifier, Output: r.Output,
		Summary:       Summary{Chunks: len(r.Plan.Chunks), Findings: map[string]int{}},
		Chunks:        make([]Chunk, len(r.Plan.Chunks)),
		Chapters:      make([]Chapter, len(r.Sections)),
		UsageEstimate: r.EstimateUSD, UsageNote: r.PriceNote,
	}
	if f := m.Verifier.Fallback; f != nil {
		f.Cause = logx.Scrub(f.Cause)
	}
	for i, pc := range r.Plan.Chunks {
		c := Chunk{
			ID: pc.ID, TextSHA256: sha(pc.Display), SpokenSHA256: sha(pc.Spoken),
			Chars: utf8.RuneCountInString(pc.Display), Lang: string(pc.Script),
			Findings: make([]Finding, len(pc.Findings)), RequestLike: r.Tasks[i].Chunk.RequestLike,
			GapType: string(pc.Gap), CacheKey: pc.CacheKey, State: "pending", AttemptLog: []AttemptRecord{},
		}
		for k, f := range pc.Findings {
			c.Findings[k] = Finding(f)
			m.Summary.Findings[f.Code]++
		}
		if c.RequestLike {
			m.Summary.RequestLikeChunks++
		}
		if i < len(r.Results) {
			fill(&c, r.Results[i])
			if i < len(r.Spans) {
				c.StartS, c.EndS = seconds(r.Spans[i].Start), seconds(r.Spans[i].End)
				c.GapAfterS = seconds(int64(r.Gaps[i]))
			}
		}
		count(&m.Summary, c)
		m.Chunks[i] = c
	}
	if err := chapters(&m, r); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// chapters fills m.Chapters from r.Sections and labels each chunk with its section.
func chapters(m *Manifest, r Run) error {
	for k, s := range r.Sections {
		if s.First < 0 || s.First >= len(r.Plan.Chunks) || k > 0 && s.First <= r.Sections[k-1].First {
			return fmt.Errorf("manifest: section %d starts at chunk %d", k+1, s.First)
		}
		ch := Chapter{Title: s.Title, FirstChunk: r.Plan.Chunks[s.First].ID}
		if s.Path != "" {
			ch.Path = &s.Path
		}
		next := len(r.Plan.Chunks)
		if k+1 < len(r.Sections) {
			next = r.Sections[k+1].First
		}
		if s.First < len(r.Spans) {
			ch.StartS = seconds(r.Spans[s.First].Start)
		}
		switch {
		case next < len(r.Spans):
			ch.EndS = seconds(r.Spans[next].Start)
		case next == len(r.Plan.Chunks) && len(r.Spans) == next:
			ch.EndS = seconds(r.Spans[next-1].End)
		}
		m.Chapters[k] = ch
		for i := s.First; i < next; i++ {
			m.Chunks[i].Section = &r.Sections[k].Title
		}
	}
	return nil
}

func fill(c *Chunk, res verify.ChunkResult) {
	c.State, c.Verified, c.Cached = string(res.State), res.State == verify.StateVerified, res.Cached
	c.Attempts, c.FinalAttempt, c.Score, c.STTCalls = len(res.Attempts), res.Final, res.Score, res.STTCalls
	for _, a := range res.Attempts {
		rec := AttemptRecord{N: a.N, Status: string(a.Status), Eligible: a.Eligible, Score: a.Score, STTScores: []float64{}}
		for _, tr := range a.Transcriptions {
			rec.STTScores = append(rec.STTScores, tr.Result.Score)
			c.Usage.STTSeconds += tr.Seconds
		}
		if a.Rejection != nil {
			rec.Rejection = &Rejection{Reason: string(a.Rejection.Reason), Detail: logx.Scrub(a.Rejection.Detail)}
		}
		c.AttemptLog = append(c.AttemptLog, rec)
		if res.Cached {
			continue // reused: nothing was rendered this run
		}
		u := a.Usage
		if u.Detailed {
			c.Usage.TextIn += u.InputTextTokens
			c.Usage.TextOut += u.OutputTextTokens
			c.Usage.AudioOut += u.OutputAudioTokens
		} else {
			c.Usage.TextIn += u.InputTokens
			c.Usage.AudioOut += u.OutputTokens
		}
	}
}

func count(s *Summary, c Chunk) {
	switch c.State {
	case string(verify.StateVerified):
		s.Verified++
	case string(verify.StateUnverified):
		s.Unverified++
	case string(verify.StateNoEligible):
		s.NoEligible++
	default:
		s.Pending++
	}
	if c.Cached {
		s.Cached++
	} else {
		s.Renders += c.Attempts
	}
	s.STTCalls += c.STTCalls
}

// Encode writes m as indented JSON with a final newline.
func (m Manifest) Encode(w io.Writer) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("manifest: encode: %w", err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("manifest: write: %w", err)
	}
	return nil
}

// Read decodes a manifest, refusing unknown fields and other schemas.
func Read(r io.Reader) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("manifest: decode: %w", err)
	}
	if m.Schema != Schema {
		return Manifest{}, fmt.Errorf("manifest: schema %q, want %q", m.Schema, Schema)
	}
	return m, nil
}
