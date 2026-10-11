package calibrate_test

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/xiaolai/recite/internal/calibrate"
	"github.com/xiaolai/recite/internal/livebudget"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/verify"
)

// fullCorpus reads a script's VER-11 corpus: for Latin the VER-07 file and its continuation.
func fullCorpus(t *testing.T, sc calibrate.Script) []string {
	t.Helper()
	files := map[calibrate.Script][]string{
		calibrate.Latin: {"testdata/passages-en.txt", "testdata/passages-en-more.txt"},
		calibrate.Han:   {"testdata/passages-zh.txt"},
	}[sc]
	var out []string
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		p, err := calibrate.ReadPassages(f)
		_ = f.Close() // read-only: a close error cannot lose data
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p...)
	}
	return out
}

func hasHanSpace(s string) bool {
	r := []rune(s)
	for i := 1; i+1 < len(r); i++ {
		if r[i] == ' ' && (unicode.Is(unicode.Han, r[i-1]) || unicode.Is(unicode.Han, r[i+1])) {
			return true
		}
	}
	return false
}

func TestParseScript(t *testing.T) {
	for in, want := range map[string]calibrate.Script{"latin": calibrate.Latin, " Han ": calibrate.Han} {
		if got, err := calibrate.ParseScript(in); err != nil || got != want {
			t.Fatalf("ParseScript(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := calibrate.ParseScript("cyrillic"); err == nil {
		t.Fatal("unknown script accepted")
	}
	if calibrate.Han.Language() != "zh" || calibrate.Latin.Language() != "en" {
		t.Fatal("script languages")
	}
}

func TestUnsegment(t *testing.T) {
	for in, want := range map[string]string{
		"林秋禾 每天 早上 七 点 出门。":   "林秋禾每天早上七点出门。",
		"打开 GPS 定位，走 3 km 路。": "打开GPS定位，走3 km路。",
		"好的， 下面 是 文字。":        "好的，下面是文字。",
	} {
		if got := calibrate.Unsegment(in); got != want {
			t.Fatalf("Unsegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCorruptHanKinds(t *testing.T) {
	rng := func() *rand.Rand { return rand.New(rand.NewPCG(5, 6)) }
	cases := []struct {
		k            calibrate.Kind
		in           string
		want, suffix string // exact result, or its required ending when the edit is a random choice
	}{
		{k: calibrate.WordSwap, in: "他 喜欢 喝茶。", want: "他喝茶喜欢。"},
		{k: calibrate.ClauseDrop, in: "今天 下雨，我们 不 出门。", want: "今天下雨。"},
		{k: calibrate.ClauseDrop, in: "早上 起床，先 喝水；然后 出门，走 到 车站。", suffix: "。"},
		{k: calibrate.Preamble, in: "他 走 了。", suffix: "他走了。"},
		{k: calibrate.NumberChange, in: "他 走 了 十一 里 路。", want: "他走了十里路。"},
		{k: calibrate.NumberChange, in: "她 等 了 41 天。", want: "她等了44天。"},
		{k: calibrate.NameChange, in: "林秋禾 笑 了。", want: "赵云舒笑了。"},  // a person stays a person,
		{k: calibrate.NameChange, in: "她 去 了 南塘。", want: "她去了北岭。"}, // a place stays a place,
		{k: calibrate.ContentWord, in: "天气 很 冷。", want: "天气很热。"},
		{k: calibrate.ContentWord, in: "我们 看见 房子。", suffix: "。"},
	}
	for _, c := range cases {
		got, err := calibrate.CorruptScript(calibrate.Han, c.in, c.k, rng())
		if err != nil {
			t.Fatalf("%s %q: %v", c.k, c.in, err)
		}
		again, _ := calibrate.CorruptScript(calibrate.Han, c.in, c.k, rng())
		if got != again {
			t.Fatalf("%s %q is not deterministic: %q, %q", c.k, c.in, got, again)
		}
		orig := calibrate.Unsegment(c.in)
		switch {
		case c.want != "" && got != c.want:
			t.Fatalf("%s %q = %q, want %q", c.k, c.in, got, c.want)
		case !strings.HasSuffix(got, c.suffix):
			t.Fatalf("%s %q = %q, want it to end %q", c.k, c.in, got, c.suffix)
		case strings.Contains(got, " "):
			t.Fatalf("%s %q = %q: segmenting spaces left", c.k, c.in, got)
		case verify.Equivalent(got, orig):
			t.Fatalf("%s %q = %q: equivalent to the original %q", c.k, c.in, got, orig)
		}
		t.Logf("%s: %s -> %s", c.k, orig, got)
	}
	for k, in := range map[calibrate.Kind]string{
		calibrate.ClauseDrop:   "没有 逗号 的 句子。",
		calibrate.NumberChange: "这里 没有 数字。",
		calibrate.NameChange:   "这里 没有 名字。",
		calibrate.WordSwap:     "好。",
		calibrate.ContentWord:  "他 是 我。",
	} {
		if _, err := calibrate.CorruptScript(calibrate.Han, in, k, rng()); !errors.Is(err, calibrate.ErrNotApplicable) {
			t.Fatalf("%s %q: %v, want ErrNotApplicable", k, in, err)
		}
	}
	if _, err := calibrate.CorruptScript("cyrillic", "x", calibrate.WordSwap, rng()); err == nil {
		t.Fatal("unknown script accepted")
	}
}

// Every Han passage takes every corruption, is between 15 and 25 seconds at the default speed,
// and every corruption of it stays a different text after the spoken form.
func TestHanCorpusTakesEveryCorruption(t *testing.T) {
	for i, p := range fullCorpus(t, calibrate.Han) {
		text := calibrate.Unsegment(p)
		if d := realtime.EstimateDuration(text, realtime.DefaultSpeed); d < 15*time.Second || d > 25*time.Second {
			t.Errorf("passage %d is %v long, want 15–25 s: %s", i+1, d.Round(time.Second), text)
		}
		for _, k := range calibrate.Corruptions {
			got, err := calibrate.CorruptScript(calibrate.Han, p, k, rand.New(rand.NewPCG(uint64(i), 7)))
			if err != nil {
				t.Errorf("passage %d %s: %v", i+1, k, err)
				continue
			}
			if verify.Equivalent(got, text) {
				t.Errorf("passage %d %s left it equivalent: %q", i+1, k, got)
			}
		}
	}
}

// The VER-11 size per script: 60 good + 60 corrupted candidates, ten of each corruption,
// split 60/40 into 72 proposal samples and 48 held-out clips (24 good, 4 of each corruption),
// each sample's construction surviving the spoken form, rendered in shards that fit the
// 30-minute live budget.
func TestFullCalibrationCandidates(t *testing.T) {
	for _, sc := range []calibrate.Script{calibrate.Latin, calibrate.Han} {
		t.Run(string(sc), func(t *testing.T) {
			p := fullCorpus(t, sc)
			s, err := calibrate.BuildScript(sc, p, calibrate.FullGood, calibrate.FullCorrupted, seed)
			if err != nil {
				t.Fatal(err)
			}
			again, _ := calibrate.BuildScript(sc, p, calibrate.FullGood, calibrate.FullCorrupted, seed)
			if !slices.Equal(s, again) {
				t.Fatal("BuildScript is not deterministic")
			}
			kinds := map[calibrate.Kind]int{}
			for _, x := range s {
				kinds[x.Kind]++
				if sc == calibrate.Han && (hasHanSpace(x.Original) || hasHanSpace(x.Rendered)) {
					t.Fatalf("%s: segmenting spaces reached the text: %q / %q", x.ID, x.Original, x.Rendered)
				}
			}
			if kinds[calibrate.Good] != 60 {
				t.Fatalf("kinds %v", kinds)
			}
			for _, k := range calibrate.Corruptions {
				if kinds[k] != 10 {
					t.Fatalf("kinds %v", kinds)
				}
			}
			prop, held := calibrate.Split(s, seed)
			if len(held) != 48 || len(prop) != 72 {
				t.Fatalf("held %d, proposal %d", len(held), len(prop))
			}
			per := map[calibrate.Kind]int{}
			for _, x := range held {
				per[x.Kind]++
			}
			if per[calibrate.Good] != 24 {
				t.Fatalf("held out per kind %v", per)
			}
			for _, k := range calibrate.Corruptions {
				if per[k] != 4 {
					t.Fatalf("held out per kind %v", per)
				}
			}

			spec, err := realtime.DefaultSettings().SessionSpec()
			if err != nil {
				t.Fatal(err)
			}
			tasks, err := calibrate.Tasks(s, spec, func(x string) (string, error) {
				r, err := spoken.Normalize(x, sc.Language(), spoken.Options{})
				return r.Spoken, err
			})
			if err != nil {
				t.Fatal(err)
			}
			var total time.Duration
			for i, tk := range tasks {
				if s[i].Corrupted() == verify.Equivalent(tk.Render, tk.Chunk.Spoken) {
					t.Fatalf("%s (%s): the spoken form undid the construction: %q vs %q", tk.Chunk.ID, s[i].Kind, tk.Render, tk.Chunk.Spoken)
				}
				total += tk.Chunk.Estimate
			}
			shards := calibrate.Shard(tasks, 30*time.Minute)
			for k, part := range shards {
				var est time.Duration
				for _, tk := range part {
					est += tk.Chunk.Estimate
				}
				if err := livebudget.Preflight(est, 1, 30*time.Minute); err != nil {
					t.Fatalf("shard %d: %v", k+1, err)
				}
			}
			t.Logf("%s: %d samples, about %v of audio, %d shards", sc, len(s), total.Round(time.Second), len(shards))
		})
	}
}

func TestScriptReport(t *testing.T) {
	d := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if got := calibrate.ScriptReportName(d, calibrate.Han); got != "calibration-20261012-han.md" {
		t.Fatal(got)
	}
	var b bytes.Buffer
	if err := calibrate.WriteReport(&b, calibrate.Report{Title: "VER-11 full calibration, han", Date: d}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.String(), "# Verifier calibration, 2026-10-12 (VER-11 full calibration, han)\n") {
		t.Fatalf("title: %q", strings.SplitN(b.String(), "\n", 2)[0])
	}
}
