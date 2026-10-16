package manifest

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/fsx"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/verify"
	"github.com/xiaolai/sayso/go/secret"
)

// RejectedSchema identifies a rejected-take report (OUT-06).
const RejectedSchema = "recite/rejected-take@1"

// RejectedReport is the .json written next to a kept rejected take: the reason, the scores,
// each transcript and its alignment against the spoken form.
type RejectedReport struct {
	Schema         string               `json:"schema"`
	Chunk          string               `json:"chunk"`
	Attempt        int                  `json:"attempt"`
	Expected       string               `json:"expected"`
	Said           string               `json:"said"`
	Status         string               `json:"status"`
	Eligible       bool                 `json:"eligible"`
	Reason         string               `json:"reason"`
	Detail         string               `json:"detail"`
	Score          float64              `json:"score"`
	Transcriptions []RejectedTranscript `json:"transcriptions"`
}

// RejectedTranscript is one STT pass over the rejected take.
type RejectedTranscript struct {
	Text      string      `json:"text"`
	Language  string      `json:"language"`
	Metric    string      `json:"metric"`
	Score     float64     `json:"score"`
	Errors    int         `json:"errors"`
	RefLen    int         `json:"ref_len"`
	Alignment []Alignment `json:"alignment"`
}

// Alignment is one substitution, insertion or deletion span.
type Alignment struct {
	Kind          string `json:"kind"`
	Expected      string `json:"expected"`
	Heard         string `json:"heard"`
	RefStart      int    `json:"ref_start"`
	RefEnd        int    `json:"ref_end"`
	LowConfidence bool   `json:"low_confidence"`
}

// RejectedName is the base name of a rejected take's files: <chunk>-a<attempt>.
func RejectedName(chunk string, attempt int) string {
	return chunk + "-a" + strconv.Itoa(attempt)
}

// WriteRejected saves take a of chunk c in dir, a private directory held open (fsx.Dir):
// <chunk>-a<attempt>.wav with the take's audio as rendered, and .json with its report. Both
// files are private (0600). Every text field is scrubbed.
func WriteRejected(dir *fsx.Dir, c verify.Chunk, a verify.Attempt, take realtime.Take) error {
	if a.Rejection == nil {
		return fmt.Errorf("manifest: %s attempt %d was not rejected", c.ID, a.N)
	}
	r := RejectedReport{
		Schema: RejectedSchema, Chunk: c.ID, Attempt: a.N, Expected: secret.Scrub(c.Spoken), Said: secret.Scrub(take.Said),
		Status: string(a.Status), Eligible: a.Eligible, Reason: string(a.Rejection.Reason),
		Detail: secret.Scrub(a.Rejection.Detail), Score: a.Score, Transcriptions: []RejectedTranscript{},
	}
	for _, tr := range a.Transcriptions {
		rt := RejectedTranscript{Text: secret.Scrub(tr.Text), Language: tr.Language, Metric: string(tr.Result.Metric),
			Score: tr.Result.Score, Errors: tr.Result.Errors, RefLen: tr.Result.RefLen, Alignment: []Alignment{}}
		low := map[int]bool{}
		for _, sc := range tr.Confidence {
			if sc.Low {
				low[sc.Edit.RefStart] = true
			}
		}
		for _, e := range tr.Result.Edits {
			rt.Alignment = append(rt.Alignment, Alignment{Kind: string(e.Kind), Expected: secret.Scrub(e.Expected),
				Heard: secret.Scrub(e.Heard), RefStart: e.RefStart, RefEnd: e.RefEnd, LowConfidence: low[e.RefStart]})
		}
		r.Transcriptions = append(r.Transcriptions, rt)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("manifest: encode rejected take: %w", err)
	}
	base := RejectedName(c.ID, a.N)
	pcm := audio.PCM(take.PCM[:len(take.PCM)-len(take.PCM)%audio.BytesPerSample])
	f, err := dir.Create(base + ".wav")
	if err != nil {
		return err
	}
	werr := audio.Write(f, audio.FormatWAV, pcm)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("manifest: write %s.wav: %w", filepath.Join(dir.Path(), base), werr)
	}
	return dir.Write(base+".json", append(b, '\n'))
}
