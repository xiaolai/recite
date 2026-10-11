package verify_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	sttfake "github.com/xiaolai/recite/internal/stt/fake"
	"github.com/xiaolai/recite/internal/verify"
)

// replayCase is one take from the live calibration runs: the passage, and what the verifier's
// recogniser wrote for it.
type replayCase struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"` // good or corrupted
	Want   string   `json:"want"` // the verify.State expected
	Reason string   `json:"reason,omitempty"`
	Note   string   `json:"note"`
	Spoken string   `json:"spoken"`
	Heard  []string `json:"heard"`
}

// TestCalibrationReplay replays failures of the live calibration runs through adjudication with
// the recorded transcripts. These cases were looked at to design the spelling variants, the
// pronoun fold, the word-order guard and the connective change, so they are regression data:
// passing them says the fixes hold, not how accurate the verifier is. Accuracy is measured on
// fresh held-out samples (VER-07, VER-11). Cases marked "-literal" use the rendered text as the
// transcript where the recorded recogniser had silently repaired it; see each note.
func TestCalibrationReplay(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "replay", "regressions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []replayCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no replay cases")
	}
	for _, tc := range cases {
		t.Run(tc.ID, func(t *testing.T) {
			c := chunk(tc.Spoken)
			var outs []sttfake.Outcome
			for _, h := range tc.Heard {
				outs = append(outs, sttfake.Text(h))
			}
			o := verify.DefaultOptions()
			o.MaxAttempts = 1
			r := run(t, c, o, synthWith(c), sttWith(outs...))
			if string(r.State) != tc.Want {
				t.Fatalf("%s take: state %s (%v, score %.4f), want %s: %s", tc.Kind, r.State, reasons(r), r.Score, tc.Want, tc.Note)
			}
			if tc.Reason != "" && string(r.Attempts[0].Rejection.Reason) != tc.Reason {
				t.Fatalf("reason %v, want %s", reasons(r), tc.Reason)
			}
		})
	}
}
