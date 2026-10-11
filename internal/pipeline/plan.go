package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/text"
)

// PlanSchema identifies the plan JSON format.
const PlanSchema = "recite/plan@1"

// CacheKeyVersion is the "v" field of the take cache key (design §11.1).
const CacheKeyVersion = 1

// Settings are the render settings that shape the audio, and so the cache key.
type Settings struct {
	Model               string
	Voice               string
	Speed               float64
	Effort              string
	InstructionsVersion int
	Emotion             string
	Style               string
	Hints               []Hint // lexicon pronunciation hints; a chunk uses those whose term it contains
}

// Hint is one lexicon pronunciation hint (design §7.8).
type Hint struct {
	Term string `json:"term"`
	Hint string `json:"hint"`
}

// Finding is a guard or spoken-form finding at a byte offset of the display text.
type Finding struct {
	Code   string
	At     int
	Action string
}

// Input is everything the planner reads. Pauses gives the exact duration of each
// text.BoundaryPause join, in chunk order.
type Input struct {
	Display  string // the display text the segmentation was made from
	Seg      text.Segmentation
	Findings []Finding
	Pauses   []time.Duration
}

// Plan is the ordered chunk list (RUN-01).
type Plan struct {
	Schema string  `json:"schema"`
	Chunks []Chunk `json:"chunks"`
}

// Chunk is one planned chunk. EstMillis is whole milliseconds, so the JSON carries no
// platform-dependent float formatting of computed values.
type Chunk struct {
	ID         string         `json:"id"`
	Display    string         `json:"display"`
	Spoken     string         `json:"spoken"`
	Script     text.Script    `json:"script"`
	Findings   []PlanFinding  `json:"findings"`
	SessionKey string         `json:"session_key"`
	Effort     string         `json:"effort"`
	Delivery   Delivery       `json:"delivery"`
	Hints      []Hint         `json:"hints"`
	CacheKey   string         `json:"cache_key"`
	EstMillis  int64          `json:"est_ms"`
	Boundary   text.Boundary  `json:"boundary"`
	Gap        audio.GapClass `json:"gap"`
	Pause      time.Duration  `json:"pause_ns,omitempty"`
}

// PlanFinding is a finding as the plan reports it, at "line:col" (col in runes).
type PlanFinding struct {
	Code   string `json:"code"`
	At     string `json:"at"`
	Action string `json:"action"`
}

// Delivery is the emotion preset and free-text style of a chunk.
type Delivery struct {
	Emotion string `json:"emotion"`
	Style   string `json:"style"`
}

// GapClassFor maps a segment boundary to its join gap (design §9.1). A cut inside a sentence
// (char, word, clause) takes the shortest class, sentence, so the split sounds as close to
// continuous as the settings allow.
func GapClassFor(b text.Boundary) (audio.GapClass, error) {
	switch b {
	case text.BoundaryChar, text.BoundaryWord, text.BoundaryClause, text.BoundarySentence:
		return audio.GapSentence, nil
	case text.BoundaryListItem:
		return audio.GapList, nil
	case text.BoundaryParagraph:
		return audio.GapParagraph, nil
	case text.BoundarySection:
		return audio.GapSection, nil
	case text.BoundaryPause:
		return audio.GapPause, nil
	case text.BoundaryEnd:
		return audio.GapNone, nil
	}
	return "", fmt.Errorf("plan: unknown boundary %q", b)
}

// SessionKey is the pool key of a chunk's realtime session (design D4).
func SessionKey(s Settings) string {
	return s.Model + "|" + s.Voice + "|" + strconv.FormatFloat(s.Speed, 'g', -1, 64)
}

// CacheKey is the take cache key over the spoken form and everything else that shapes the
// audio (design §11.1). Display text, findings and gap settings are not part of it.
func CacheKey(s Settings, hints []Hint, spoken string) string {
	pairs := make([][2]string, len(hints))
	for i, h := range hints {
		pairs[i] = [2]string{h.Term, h.Hint}
	}
	slices.SortFunc(pairs, func(a, b [2]string) int {
		if c := strings.Compare(a[0], b[0]); c != 0 {
			return c
		}
		return strings.Compare(a[1], b[1])
	})
	// Canonical JSON: fields in sorted key order, encoding/json's fixed formatting.
	k := struct {
		Effort              string      `json:"effort"`
		Emotion             string      `json:"emotion"`
		Hints               [][2]string `json:"hints"`
		InstructionsVersion int         `json:"instructions_version"`
		Model               string      `json:"model"`
		Speed               float64     `json:"speed"`
		Style               string      `json:"style"`
		Text                string      `json:"text"`
		V                   int         `json:"v"`
		Voice               string      `json:"voice"`
	}{s.Effort, s.Emotion, pairs, s.InstructionsVersion, s.Model, s.Speed, s.Style, spoken, CacheKeyVersion, s.Voice}
	b, err := json.Marshal(k)
	if err != nil {
		panic(fmt.Sprintf("plan: cache key encoding: %v", err)) // only strings and finite numbers
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s Settings) validate() error {
	if s.Model == "" || s.Voice == "" {
		return errors.New("plan: model and voice are required")
	}
	if !(s.Speed > 0) || math.IsInf(s.Speed, 0) {
		return fmt.Errorf("plan: speed %v must be positive", s.Speed)
	}
	return nil
}

// Build makes the plan. It is pure: the same input and settings give the same plan.
func Build(in Input, s Settings) (Plan, error) {
	if err := s.validate(); err != nil {
		return Plan{}, err
	}
	p := Plan{Schema: PlanSchema, Chunks: make([]Chunk, 0, len(in.Seg.Chunks))}
	pauses := in.Pauses
	for i, c := range in.Seg.Chunks {
		gap, err := GapClassFor(c.Boundary)
		if err != nil {
			return Plan{}, err
		}
		var pause time.Duration
		if gap == audio.GapPause {
			if len(pauses) == 0 {
				return Plan{}, fmt.Errorf("plan: chunk %d ends at a pause with no duration", i+1)
			}
			pause, pauses = pauses[0], pauses[1:]
		}
		hints := []Hint{}
		for _, h := range s.Hints {
			if h.Term != "" && strings.Contains(c.Text, h.Term) {
				hints = append(hints, h)
			}
		}
		p.Chunks = append(p.Chunks, Chunk{
			ID:         fmt.Sprintf("c%04d", i+1),
			Display:    c.DisplayText,
			Spoken:     c.Text,
			Script:     c.Script,
			Findings:   chunkFindings(in, c),
			SessionKey: SessionKey(s),
			Effort:     s.Effort,
			Delivery:   Delivery{Emotion: s.Emotion, Style: s.Style},
			Hints:      hints,
			CacheKey:   CacheKey(s, hints, c.Text),
			EstMillis:  int64(math.Round(c.Seconds * 1000)),
			Boundary:   c.Boundary,
			Gap:        gap,
			Pause:      pause,
		})
	}
	if len(pauses) > 0 {
		return Plan{}, fmt.Errorf("plan: %d pause duration(s) without a pause boundary", len(pauses))
	}
	return p, nil
}

// chunkFindings returns the findings inside the chunk's display range, separator included.
func chunkFindings(in Input, c text.Chunk) []PlanFinding {
	out := []PlanFinding{}
	for _, f := range in.Findings {
		if f.At >= c.Display.Start && f.At < max(c.DisplaySep.End, c.Display.End) {
			out = append(out, PlanFinding{Code: f.Code, At: lineCol(in.Display, f.At), Action: f.Action})
		}
	}
	return out
}

func lineCol(s string, off int) string {
	off = min(max(off, 0), len(s))
	line := 1 + strings.Count(s[:off], "\n")
	col := 1 + utf8.RuneCountInString(s[strings.LastIndexByte(s[:off], '\n')+1:off])
	return strconv.Itoa(line) + ":" + strconv.Itoa(col)
}

// JSON is the plan's canonical encoding: byte-identical for identical plans.
func (p Plan) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("plan: encode: %w", err)
	}
	return append(b, '\n'), nil
}

// Joins returns the assembly joins of the plan.
func (p Plan) Joins() []audio.Join {
	out := make([]audio.Join, len(p.Chunks))
	for i, c := range p.Chunks {
		out[i] = audio.Join{CacheKey: c.CacheKey, Class: c.Gap, Pause: c.Pause}
	}
	return out
}
