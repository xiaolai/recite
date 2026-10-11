package verify_test

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/realtime"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/stt"
	sttfake "github.com/xiaolai/recite/internal/stt/fake"
	"github.com/xiaolai/recite/internal/text"
	"github.com/xiaolai/recite/internal/verify"
)

const id = "c0001"

// filler is 59 distinct content words; with one more word it makes the 60-word chunks of
// VER-06, where one wrong word scores 59/60 = 0.983.
var filler = strings.Fields(`river garden yellow window travel market silver morning
	teacher pocket orange castle bridge winter forest kitchen letter mountain painter island
	harbor village candle feather ladder blanket lantern meadow saddle thunder pepper rocket
	basket button carpet dragon engine finger glacier hammer jacket kettle lemon magnet napkin
	oyster parrot quilt rabbit salmon tunnel umbrella velvet wagon walnut zipper anchor bottle
	cactus`)

func sixty(last string) string { return strings.Join(filler, " ") + " " + last + "." }

func chunk(spokenText string) verify.Chunk {
	return verify.Chunk{
		ID:       id,
		Spoken:   spokenText,
		Language: text.Resolve(spokenText, text.ResolveOptions{}),
		Estimate: realtime.EstimateDuration(spokenText, 1),
	}
}

// run adjudicates c against the scripted synthesizer and transcriber.
func run(t *testing.T, c verify.Chunk, o verify.Options, synth *rtfake.ScriptedSynthesizer, tr stt.Transcriber) verify.ChunkResult {
	t.Helper()
	ctx := context.Background()
	sess, err := synth.Open(ctx, realtime.SessionSpec{})
	if err != nil {
		t.Fatal(err)
	}
	render := func(ctx context.Context, n int) (realtime.Take, error) {
		return sess.Render(ctx, realtime.RenderRequest{ChunkID: c.ID, Text: c.Spoken, Attempt: n})
	}
	res, err := verify.Adjudicate(ctx, c, o, render, tr)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func synthWith(c verify.Chunk, outs ...rtfake.Outcome) *rtfake.ScriptedSynthesizer {
	s := rtfake.NewScriptedSynthesizer()
	s.Default = rtfake.Pass(c.Estimate)
	return s.On(c.ID, outs...)
}

func sttWith(outs ...sttfake.Outcome) *sttfake.ScriptedTranscriber {
	return sttfake.NewScriptedTranscriber().On(id, outs...)
}

// noSTT fails the test if anything is transcribed.
type noSTT struct{ t *testing.T }

func (n noSTT) Transcribe(context.Context, []byte, stt.Hint) (stt.Transcript, error) {
	n.t.Fatal("unexpected STT call")
	return stt.Transcript{}, nil
}

func reasons(r verify.ChunkResult) []verify.Reason {
	var out []verify.Reason
	for _, a := range r.Attempts {
		if a.Rejection == nil {
			out = append(out, "")
			continue
		}
		out = append(out, a.Rejection.Reason)
	}
	return out
}

func wantResult(t *testing.T, r verify.ChunkResult, state verify.State, attempts, sttCalls int, rs ...verify.Reason) {
	t.Helper()
	if r.State != state || len(r.Attempts) != attempts || r.STTCalls != sttCalls {
		t.Fatalf("state %s, %d attempts, %d STT calls (%v); want %s, %d, %d", r.State, len(r.Attempts), r.STTCalls, reasons(r), state, attempts, sttCalls)
	}
	if rs != nil {
		got := reasons(r)
		for i := range rs {
			if got[i] != rs[i] {
				t.Fatalf("reasons %v, want %v", got, rs)
			}
		}
	}
}

// accept: VER-05#1
// accept: VER-05#2
func TestPreSTTGuardsRerenderWithoutSTT(t *testing.T) {
	c := chunk("The quick brown fox jumps over the lazy dog near the riverbank.")
	cases := []struct {
		name   string
		out    rtfake.Outcome
		reason verify.Reason
	}{
		{"said", rtfake.Say("Here is a poem about foxes instead.", c.Estimate), verify.ReasonSaid},
		{"empty", rtfake.Pass(100 * time.Millisecond), verify.ReasonEmpty},
		{"duration short", rtfake.Pass(c.Estimate * 4 / 10), verify.ReasonDuration},
		{"duration long", rtfake.Pass(c.Estimate * 23 / 10), verify.ReasonDuration},
		{"status", rtfake.Outcome{Echo: true, Duration: c.Estimate, Status: realtime.StatusFailed}, verify.ReasonStatus},
		{"cancelled", rtfake.Outcome{Echo: true, Duration: c.Estimate, Status: realtime.StatusCancelled}, verify.ReasonStatus},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synth := synthWith(c, tc.out)
			tr := sttWith(sttfake.Text(c.Spoken))
			r := run(t, c, verify.DefaultOptions(), synth, tr)
			wantResult(t, r, verify.StateVerified, 2, 1, tc.reason, "")
			if synth.Attempts(id) != 2 || r.Final != 2 {
				t.Fatalf("rendered %d times, kept attempt %d", synth.Attempts(id), r.Final)
			}
			if r.Attempts[0].Eligible || len(r.Attempts[0].Transcriptions) != 0 {
				t.Fatal("a take rejected before STT was transcribed or marked eligible")
			}
			if calls := tr.Calls(); calls[0].TakeAttempt != 2 {
				t.Fatalf("STT heard attempt %d, want only attempt 2", calls[0].TakeAttempt)
			}
		})
	}
}

// accept: VER-05#2
// accept: VER-09#1
func TestVerifyOffRunsGuardsOnly(t *testing.T) {
	c := chunk("今天天气很好。")
	c.Language = text.Language{Code: "zh", Source: text.SourceFlag, Certain: true}
	o := verify.DefaultOptions()
	o.Mode = verify.ModeOff
	r := run(t, c, o, synthWith(c, rtfake.Pass(50*time.Millisecond)), noSTT{t})
	wantResult(t, r, verify.StateUnverified, 2, 0, verify.ReasonEmpty, "")
	if code, write := verify.Outcome([]verify.ChunkResult{r}, verify.OutputPolicy{}); code != exitcode.VerifyFailed || !write {
		t.Fatalf("verify off outcome %d, %v", code, write)
	}
}

// accept: VER-05#3
// accept: VER-05#6
func TestStatusCarriedAndIncompleteRejected(t *testing.T) {
	c := chunk("The quick brown fox jumps over the lazy dog near the riverbank.")
	take := realtime.Take{PCM: rtfake.TaggedPCM(id, 1, 2*time.Second), Said: c.Spoken, Status: realtime.StatusCompleted}
	c.Estimate = 2 * time.Second
	if _, rej := verify.PreSTT(c, take, false); rej != nil {
		t.Fatalf("completed take rejected: %+v", rej)
	}
	take.Status = realtime.StatusIncomplete
	if _, rej := verify.PreSTT(c, take, false); rej == nil || rej.Reason != verify.ReasonStatus {
		t.Fatalf("incomplete take with 2 s of audio: %+v, want status rejection", rej)
	}
	o := verify.DefaultOptions()
	o.MaxAttempts = 1
	r := run(t, c, o, synthWith(c, rtfake.Outcome{Echo: true, Duration: 2 * time.Second, Status: realtime.StatusIncomplete}), noSTT{t})
	if r.Attempts[0].Status != realtime.StatusIncomplete || r.State != verify.StateNoEligible {
		t.Fatalf("attempt status %q, state %s", r.Attempts[0].Status, r.State)
	}
}

// e22Take reads the recorded answer-instead-of-read scenario: the text sent and what the model
// said instead.
func e22Take(t *testing.T) (sent, said string) {
	t.Helper()
	f, err := os.Open(filepath.Join(rtfake.FixtureDir(), "answer-instead-of-read.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		var line struct {
			Event struct {
				Type       string `json:"type"`
				Transcript string `json:"transcript"`
				Response   struct {
					Input []struct {
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"input"`
				} `json:"response"`
			} `json:"event"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		switch line.Event.Type {
		case "response.create":
			sent = line.Event.Response.Input[0].Content[0].Text
		case "response.output_audio_transcript.done":
			said = line.Event.Transcript
		}
	}
	if err := sc.Err(); err != nil || sent == "" || said == "" {
		t.Fatalf("fixture: sent %q, said %q, err %v", sent, said, err)
	}
	return sent, said
}

// accept: VER-05#4
func TestSaidGuardRejectsRecordedAnswer(t *testing.T) {
	sent, said := e22Take(t)
	c := chunk(sent)
	take := realtime.Take{PCM: rtfake.TaggedPCM(id, 1, c.Estimate), Said: said, Status: realtime.StatusCompleted}
	if _, rej := verify.PreSTT(c, take, false); rej == nil || rej.Reason != verify.ReasonSaid {
		t.Fatalf("E22 answer %q for %q: %+v, want said rejection", said, sent, rej)
	}
}

// accept: VER-05#5
func TestSaidGuardAcceptsExpandedUnit(t *testing.T) {
	form, err := spoken.Normalize("The parcel weighs 5kg.", "en", spoken.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := chunk(form.Spoken)
	for _, said := range []string{"The parcel weighs 5 kilograms.", "The parcel weighs five kilograms."} {
		take := realtime.Take{PCM: rtfake.TaggedPCM(id, 1, c.Estimate), Said: said, Status: realtime.StatusCompleted}
		if _, rej := verify.PreSTT(c, take, false); rej != nil {
			t.Fatalf("spoken %q, said %q: %+v", form.Spoken, said, rej)
		}
	}
}

// accept: VER-05#7
func TestLanguageGuardRejectsWrongLanguage(t *testing.T) {
	c := chunk("今天天气很好，我们去公园散步吧。")
	c.Language = text.Language{Code: "zh", Source: text.SourceFlag, Certain: true}
	o := verify.DefaultOptions()
	o.MaxAttempts = 1
	tr := sttWith(sttfake.Text(c.Spoken, "en"))
	r := run(t, c, o, synthWith(c), tr)
	wantResult(t, r, verify.StateUnverified, 1, 1, verify.ReasonLanguage)
	if h := tr.Calls()[0].Hint.Languages; len(h) != 1 || h[0] != "zh" {
		t.Fatalf("hint %v, want [zh]", h)
	}
}

func TestLanguageGuardSkipsWhenNoLanguageReported(t *testing.T) {
	c := chunk("今天天气很好，我们去公园散步吧。")
	c.Language = text.Language{Code: "zh", Source: text.SourceFlag, Certain: true}
	// The realtime gpt-live-transcribe fallback reports no language.
	r := run(t, c, verify.DefaultOptions(), synthWith(c), sttWith(sttfake.Text(c.Spoken)))
	wantResult(t, r, verify.StateVerified, 1, 1, "")
	if verify.LanguageGuard(c.Language, stt.Transcript{Languages: []stt.DetectedLanguage{{Code: ""}}}) != nil {
		t.Fatal("an empty language code was judged")
	}
	if verify.LanguageGuard(c.Language, stt.Transcript{Languages: []stt.DetectedLanguage{{Code: "zh-CN"}}}) != nil {
		t.Fatal("zh-CN rejected for zh")
	}
}

// accept: TXT-04#2
func TestUncertainLanguageNotRejected(t *testing.T) {
	c := chunk("Bonjour, comment allez-vous ?")
	if c.Language.Code != "en" || c.Language.Certain {
		t.Fatalf("language %+v, want en, not certain", c.Language)
	}
	tr := sttWith(sttfake.Text(c.Spoken, "fr"))
	r := run(t, c, verify.DefaultOptions(), synthWith(c), tr)
	wantResult(t, r, verify.StateVerified, 1, 1, "")
	if h := tr.Calls()[0].Hint.Languages; len(h) != 0 {
		t.Fatalf("hint %v, want none", h)
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.0005 }

// accept: VER-06#1
func TestEdgeInsertionGuard(t *testing.T) {
	body := strings.Join(append(filler, filler[:41]...), " ") // 100 words
	for _, heard := range []string{"Sure, here it is. " + body, body + " Hope that helps!" + " " + "Bye"} {
		c := chunk(body)
		o := verify.DefaultOptions()
		o.MaxAttempts = 1
		r := run(t, c, o, synthWith(c), sttWith(sttfake.Text(heard)))
		wantResult(t, r, verify.StateUnverified, 1, 1, verify.ReasonEdgeInsertion)
		if !near(r.Attempts[0].Score, 0.96) {
			t.Fatalf("score %.4f, want 0.96", r.Attempts[0].Score)
		}
	}
	c := chunk(body)
	r := run(t, c, verify.DefaultOptions(), synthWith(c), sttWith(sttfake.Text("The "+body)))
	wantResult(t, r, verify.StateVerified, 1, 1, "")
}

// accept: VER-06#2
func TestProtectedNumber(t *testing.T) {
	c := chunk(strings.Join(filler[:58], " ") + " forty-two.") // 60 canonical words
	for _, mode := range []verify.Mode{verify.ModeNormal, verify.ModeStrict} {
		o := verify.Options{Mode: mode, MaxAttempts: 1}
		r := run(t, c, o, synthWith(c), sttWith(sttfake.Text(strings.Join(filler[:58], " ")+" forty-three.")))
		wantResult(t, r, verify.StateUnverified, 1, 1, verify.ReasonProtected)
		if !near(r.Attempts[0].Score, 0.983) {
			t.Fatalf("score %.4f, want 0.983", r.Attempts[0].Score)
		}
	}
	// Digits from the recogniser are the same number.
	r := run(t, c, verify.DefaultOptions(), synthWith(c), sttWith(sttfake.Text(strings.Join(filler[:58], " ")+" 42.")))
	wantResult(t, r, verify.StateVerified, 1, 1, "")
}

// accept: VER-06#3
func TestProtectedName(t *testing.T) {
	c := chunk(strings.Join(filler, " ") + " Okafor.")
	o := verify.DefaultOptions()
	o.MaxAttempts = 1
	r := run(t, c, o, synthWith(c), sttWith(sttfake.Text(strings.Join(filler, " ")+" Okafur.")))
	wantResult(t, r, verify.StateUnverified, 1, 1, verify.ReasonProtected)
}

func TestProtectedLexiconAndMarkSpans(t *testing.T) {
	base := strings.Join(filler[:58], " ")
	c := chunk(base + " kubectl apply.")
	c.Lexicon = []string{"kubectl"}
	o := verify.DefaultOptions()
	o.MaxAttempts = 1
	r := run(t, c, o, synthWith(c), sttWith(sttfake.Text(base+" cube cuddle apply.")))
	wantResult(t, r, verify.StateUnverified, 1, 1, verify.ReasonProtected)

	c = chunk(base + " see ay.")
	at := strings.Index(c.Spoken, "see ay")
	c.Protected = []spoken.Range{{Start: at, End: at + len("see ay")}}
	r = run(t, c, o, synthWith(c), sttWith(sttfake.Text(base+" sea ay.")))
	wantResult(t, r, verify.StateUnverified, 1, 1, verify.ReasonProtected)
}

// accept: VER-06#4
// accept: VER-06#5
// accept: VER-06#6
// accept: VER-06#7
func TestContentWordGuard(t *testing.T) {
	approved := chunk(sixty("approved"))
	rejected := sixty("rejected")
	midNot := strings.Join(filler[:30], " ") + " not " + strings.Join(filler[30:], " ") + " approved."
	zh := chunk("今天我们不去公园散步因为外面正在下大雨所以大家都留在家里看书写字喝茶聊天做饭打扫房间整理衣服明天天气好再出门去郊外爬山吧")
	if n := len([]rune(zh.Spoken)); n != 60 {
		t.Fatalf("Chinese chunk has %d runes, want 60", n)
	}
	zhNoNot := strings.Replace(zh.Spoken, "不", "", 1)
	cases := []struct {
		name  string
		c     verify.Chunk
		heard []string
		state verify.State
		calls int
	}{
		{"substitution heard twice", approved, []string{rejected, rejected}, verify.StateUnverified, 2},
		{"substitution heard once", approved, []string{rejected, approved.Spoken}, verify.StateVerified, 2},
		{"inserted not heard twice", approved, []string{midNot, midNot}, verify.StateUnverified, 2},
		{"inserted not heard once", approved, []string{midNot, approved.Spoken}, verify.StateVerified, 2},
		{"Chinese 不 deleted twice", zh, []string{zhNoNot, zhNoNot}, verify.StateUnverified, 2},
		{"function word", chunk(sixty("a")), []string{sixty("the")}, verify.StateVerified, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var outs []sttfake.Outcome
			for _, h := range tc.heard {
				outs = append(outs, sttfake.Text(h))
			}
			o := verify.DefaultOptions()
			o.MaxAttempts = 1
			r := run(t, tc.c, o, synthWith(tc.c), sttWith(outs...))
			if r.State != tc.state || r.STTCalls != tc.calls {
				t.Fatalf("state %s with %d STT calls (%v), want %s with %d", r.State, r.STTCalls, reasons(r), tc.state, tc.calls)
			}
			if tc.state == verify.StateUnverified && r.Attempts[0].Rejection.Reason != verify.ReasonContentWord {
				t.Fatalf("reason %v", reasons(r))
			}
			if s := r.Attempts[0].Transcriptions[0].Result.Score; !near(s, 0.983) {
				t.Fatalf("first score %.4f, want 0.983", s)
			}
		})
	}
}

// pathRef is all function-word noise apart from its content words, so substituting function
// words lowers the score without arming the content-word or protected-token guards. Each
// error costs 0.05.
const pathRef = "we sat in the box at the top of the hill by the sea and he was there with them"

var (
	pathGray = strings.NewReplacer(" in ", " on ", " at ", " by ").Replace(pathRef)                                    // 0.90
	pathLow  = strings.NewReplacer(" in ", " on ", " at ", " by ", " of ", " from ", " and ", " or ").Replace(pathRef) // 0.80
)

// accept: VER-08#1
// accept: VER-08#2
// accept: VER-08#3
// accept: VER-08#4
// accept: VER-08#5
// accept: VER-08#6
func TestAdjudicationPaths(t *testing.T) {
	c := chunk(pathRef)
	answer := rtfake.Say("Sure! Here is a story about a box.", c.Estimate)
	empty := rtfake.Pass(50 * time.Millisecond)
	long := rtfake.Pass(c.Estimate * 3)
	text := sttfake.Text
	cases := []struct {
		name     string
		takes    []rtfake.Outcome
		heard    []string
		state    verify.State
		attempts int
		calls    int
		final    int
	}{
		{"pass first time", nil, []string{pathRef}, verify.StateVerified, 1, 1, 1},
		{"gray then pass on the second STT", nil, []string{pathGray, pathRef}, verify.StateVerified, 1, 2, 1},
		{"gray twice then re-render then pass", nil, []string{pathGray, pathGray, pathRef}, verify.StateVerified, 2, 3, 2},
		{"three rejects keep the best eligible take", nil, []string{pathLow, pathGray, pathGray, pathLow}, verify.StateUnverified, 3, 4, 2},
		{"guard reject then STT pass", []rtfake.Outcome{answer}, []string{pathRef}, verify.StateVerified, 2, 1, 2},
		{"every attempt empty", []rtfake.Outcome{empty, empty, empty}, nil, verify.StateNoEligible, 3, 0, 0},
		{"every attempt an answer", []rtfake.Outcome{answer, answer, answer}, nil, verify.StateNoEligible, 3, 0, 0},
		{"every attempt too long", []rtfake.Outcome{long, long, long}, nil, verify.StateNoEligible, 3, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var outs []sttfake.Outcome
			for _, h := range tc.heard {
				outs = append(outs, text(h))
			}
			r := run(t, c, verify.DefaultOptions(), synthWith(c, tc.takes...), sttWith(outs...))
			wantResult(t, r, tc.state, tc.attempts, tc.calls)
			if r.Final != tc.final {
				t.Fatalf("kept attempt %d, want %d", r.Final, tc.final)
			}
			if tc.state == verify.StateNoEligible {
				if r.PCM != nil {
					t.Fatal("audio kept for a chunk with no eligible take")
				}
				if code, write := verify.Outcome([]verify.ChunkResult{r}, verify.OutputPolicy{AllowUnverified: true}); code != exitcode.VerifyFailed || write {
					t.Fatalf("outcome %d, write %v; want 5, no file", code, write)
				}
				return
			}
			if len(r.PCM) == 0 || !near(r.Score, r.Attempts[r.Final-1].Score) {
				t.Fatalf("kept %d bytes at score %.4f", len(r.PCM), r.Score)
			}
		})
	}
}

// accept: VER-08#1
// accept: TXT-14#2
func TestRequestLikeExtraAttempt(t *testing.T) {
	for _, flagged := range []bool{false, true} {
		c := chunk(pathRef)
		c.RequestLike = flagged
		synth := synthWith(c)
		r := run(t, c, verify.DefaultOptions(), synth, sttWith(sttfake.Text(pathLow), sttfake.Text(pathLow), sttfake.Text(pathLow), sttfake.Text(pathLow)))
		want := verify.DefaultMaxAttempts
		if flagged {
			want++
		}
		if synth.Attempts(id) != want || r.State != verify.StateUnverified {
			t.Fatalf("request-like %v: %d renders (%s), want %d", flagged, synth.Attempts(id), r.State, want)
		}
	}
}

// accept: VER-09#1
// accept: CLI-03#1
func TestOutcomeTable(t *testing.T) {
	verified := verify.ChunkResult{State: verify.StateVerified}
	unverified := verify.ChunkResult{State: verify.StateUnverified}
	none := verify.ChunkResult{State: verify.StateNoEligible}
	policies := []verify.OutputPolicy{{}, {AllowUnverified: true}, {StrictOutput: true}}
	type cell struct {
		code  int
		write bool
	}
	rows := []struct {
		name    string
		results []verify.ChunkResult
		want    [3]cell
	}{
		{"all verified", []verify.ChunkResult{verified, verified}, [3]cell{{0, true}, {0, true}, {0, true}}},
		{"some unverified", []verify.ChunkResult{verified, unverified}, [3]cell{{5, true}, {0, true}, {5, false}}},
		{"some no eligible take", []verify.ChunkResult{unverified, none, verified}, [3]cell{{5, false}, {5, false}, {5, false}}},
	}
	for _, row := range rows {
		for i, p := range policies {
			code, write := verify.Outcome(row.results, p)
			if (cell{code, write}) != row.want[i] {
				t.Errorf("%s with %+v: %d, %v; want %+v", row.name, p, code, write, row.want[i])
			}
		}
	}
	if code, write := verify.Outcome([]verify.ChunkResult{unverified}, verify.OutputPolicy{AllowUnverified: true, StrictOutput: true}); code != exitcode.VerifyFailed || write {
		t.Errorf("both flags: %d, %v; want 5, no file", code, write)
	}
}

func TestLowConfidenceSpans(t *testing.T) {
	c := chunk(pathRef)
	o := verify.DefaultOptions()
	o.MaxAttempts = 1
	heard := pathLow
	tr := sttfake.NewScriptedTranscriber()
	var toks []stt.TokenLogprob
	for _, w := range strings.SplitAfter(heard, " ") {
		lp := -0.1
		if strings.TrimSpace(w) == "from" {
			lp = -2.5
		}
		toks = append(toks, stt.TokenLogprob{Token: w, Logprob: lp})
	}
	tr.On(id, sttfake.Outcome{Transcript: stt.Transcript{Text: heard, Tokens: toks}})
	r := run(t, c, o, synthWith(c), tr)
	var low []string
	for _, sc := range r.Attempts[0].Transcriptions[0].Confidence {
		if sc.Low {
			low = append(low, sc.Edit.Heard)
		}
	}
	if len(low) != 1 || low[0] != "from" {
		t.Fatalf("low-confidence spans %q, want [from]", low)
	}
}

func TestAdjudicateRejectsBadOptions(t *testing.T) {
	c := chunk(pathRef)
	for _, o := range []verify.Options{{Mode: "loose", MaxAttempts: 3}, {Mode: verify.ModeNormal}, {Mode: verify.ModeNormal, MaxAttempts: 1, RequestExtraAttempts: -1}} {
		if _, err := verify.Adjudicate(context.Background(), c, o, nil, noSTT{t}); err == nil {
			t.Errorf("options %+v accepted", o)
		}
	}
	c.Estimate = 0
	if _, err := verify.Adjudicate(context.Background(), c, verify.DefaultOptions(), nil, noSTT{t}); err == nil {
		t.Error("chunk without an estimate accepted")
	}
}

// The STT clients pad each take with silence themselves (design §8.2), so adjudication must
// hand them the trimmed take as is; padding here too doubled the silence.
func TestTranscriberGetsTheUnpaddedTake(t *testing.T) {
	c := chunk(sixty("approved"))
	tr := sttWith(sttfake.Text(c.Spoken))
	r := run(t, c, verify.DefaultOptions(), synthWith(c), tr)
	calls := tr.Calls()
	if r.State != verify.StateVerified || len(calls) != 1 {
		t.Fatalf("state %s with %d STT calls", r.State, len(calls))
	}
	if calls[0].Bytes != len(r.PCM) {
		t.Fatalf("transcriber got %d bytes, want the %d-byte trimmed take", calls[0].Bytes, len(r.PCM))
	}
}

// spokenChunk is the chunk recite renders for display text s: its spoken form.
func spokenChunk(t *testing.T, s string) verify.Chunk {
	t.Helper()
	r, err := spoken.Normalize(s, "en", spoken.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return chunk(r.Spoken)
}

// STT writes numbers, dates and symbols as digits; the reference is the spoken form. The
// transcript goes through the same rules, so a correct take scores as one (the live corpus
// of TXT-11#3 failed half its chunks on this) and a wrong number is still caught.
func TestTranscriptInWrittenFormMatchesSpokenReference(t *testing.T) {
	for _, tc := range []struct{ display, heard string }{
		{"In 2026 the fee rose from ¥3,000 to ¥3,500.", "In 2026, the fee rose from 3,000 yen to 3,500 yen."},
		{"Meet me on October 11, 2026 at 3:15pm in Room 204.", "Meet me on October 11, 2026 at 3:15 p.m. in room 204."},
		{"Volume III arrives on 2026-12-01 for $29.99.", "Volume III arrives on December 1, 2026, for $29.99."},
		{"The team scored 3–5 points per game in the 1990s.", "The team scored three to five points per game in the 1990s."},
	} {
		c := spokenChunk(t, tc.display)
		r := run(t, c, verify.DefaultOptions(), synthWith(c), sttWith(sttfake.Text(tc.heard)))
		if r.State != verify.StateVerified || r.Score < 0.95 {
			t.Errorf("%q heard as %q: %s, score %.3f %v", c.Spoken, tc.heard, r.State, r.Score, reasons(r))
		}
	}
}

func TestWrongNumberInWrittenFormIsStillRejected(t *testing.T) {
	c := spokenChunk(t, "In 2026 the fee rose from ¥3,000 to ¥3,500.")
	heard := "In 2025, the fee rose from 3,000 yen to 3,500 yen."
	o := verify.DefaultOptions()
	o.MaxAttempts = 1
	r := run(t, c, o, synthWith(c), sttWith(sttfake.Text(heard)))
	wantResult(t, r, verify.StateUnverified, 1, 1, verify.ReasonProtected)
	// Edits point into the transcript as STT wrote it, so logprobs can be matched to them.
	for _, e := range r.Attempts[0].Transcriptions[0].Result.Edits {
		if e.HeardEnd > len(heard) || e.HeardStart > e.HeardEnd || heard[e.HeardStart:e.HeardEnd] != e.Heard {
			t.Fatalf("edit %+v does not point into the transcript %q", e, heard)
		}
		if e.RefEnd > len(c.Spoken) || c.Spoken[e.RefStart:e.RefEnd] != e.Expected {
			t.Fatalf("edit %+v does not point into the reference %q", e, c.Spoken)
		}
	}
}

// With the spoken form off, the reference keeps its digits and a transcript in words still
// matches it.
func TestDigitReferenceMatchesWordTranscript(t *testing.T) {
	c := chunk("In 2026 the fee rose to 3,500 yen.")
	r := run(t, c, verify.DefaultOptions(), synthWith(c), sttWith(sttfake.Text("In twenty twenty-six the fee rose to three thousand five hundred yen.")))
	if r.State != verify.StateVerified {
		t.Fatalf("%s, score %.3f %v", r.State, r.Score, reasons(r))
	}
}

// The calibration option skips only the said guard: the take is transcribed and the would-be
// rejection is recorded; the default path still rejects before STT.
func TestBypassSaidReachesSTT(t *testing.T) {
	sent, said := e22Take(t)
	c := chunk(sent)
	take := realtime.Take{PCM: rtfake.TaggedPCM(id, 1, c.Estimate), Said: said, Status: realtime.StatusCompleted}
	ctx := context.Background()

	a, calls, err := verify.Check(ctx, c, verify.DefaultOptions(), 1, take, noSTT{t})
	if err != nil || calls != 0 || a.Rejection == nil || a.Rejection.Reason != verify.ReasonSaid || a.SaidMismatch {
		t.Fatalf("default: %+v, %d calls, %v; want a said rejection without STT", a.Rejection, calls, err)
	}

	o := verify.DefaultOptions()
	o.BypassSaid = true
	a, calls, err = verify.Check(ctx, c, o, 1, take, sttWith(sttfake.Text(said)))
	if err != nil || calls == 0 || !a.Eligible || !a.SaidMismatch || len(a.Transcriptions) == 0 {
		t.Fatalf("bypass: eligible %v, said mismatch %v, %d calls, %v; want transcribed", a.Eligible, a.SaidMismatch, calls, err)
	}
	if a.Rejection == nil {
		t.Fatalf("bypass: the transcript %q of an answer to %q was accepted", said, sent)
	}
}
