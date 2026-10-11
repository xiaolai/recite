package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/exitcode"
)

// dc returns n samples of constant value v.
func dc(n int, v int16) PCM {
	p := make(PCM, n*BytesPerSample)
	for i := range n {
		binary.LittleEndian.PutUint16(p[i*BytesPerSample:], uint16(v))
	}
	return p
}

func assemble(t *testing.T, chunks []PCM, gaps []int, order []int) []byte {
	t.Helper()
	var buf bytes.Buffer
	a := NewAssembler(&buf, gaps)
	for _, i := range order {
		if err := a.Add(i, chunks[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if a.Written() != int64(buf.Len()) {
		t.Fatalf("Written %d, buffer %d", a.Written(), buf.Len())
	}
	return buf.Bytes()
}

func keys(n int, tag string) []Join {
	js := make([]Join, n)
	classes := []GapClass{GapSentence, GapList, GapParagraph, GapSection}
	for i := range js {
		js[i] = Join{CacheKey: fmt.Sprintf("%s-%04d", tag, i), Class: classes[i%len(classes)]}
	}
	return js
}

// accept: AUD-03#1
func TestAssembledLengthExact(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	chunks := make([]PCM, 7)
	sum := 0
	for i := range chunks {
		chunks[i] = dc(1+r.Intn(30000), 1000)
		sum += len(chunks[i])
	}
	gaps, err := GapSamples(DefaultGaps(), keys(len(chunks), "k"))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gaps {
		sum += g * BytesPerSample
	}
	if gaps[len(gaps)-1] != 0 {
		t.Fatalf("gap after the last chunk: %d", gaps[len(gaps)-1])
	}
	out := assemble(t, chunks, gaps, []int{0, 1, 2, 3, 4, 5, 6})
	if len(out) != sum {
		t.Fatalf("assembled %d bytes, want Σ chunks + Σ gaps = %d", len(out), sum)
	}
}

// accept: AUD-03#2
func TestGapDrawsWithinRange(t *testing.T) {
	g := Gaps{
		Sentence:  GapRange{200 * time.Millisecond, 350 * time.Millisecond},
		List:      GapRange{0, 0},
		Paragraph: GapRange{600 * time.Millisecond, 600 * time.Millisecond},
		Section:   GapRange{1100 * time.Millisecond, 1500 * time.Millisecond},
	}
	js := keys(400, "w")
	gaps, err := GapSamples(g, js)
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range gaps[:len(gaps)-1] {
		r, _ := g.rangeOf(js[i].Class)
		lo, hi := samplesOf(r.Min), samplesOf(r.Max)
		if n < lo || n > hi {
			t.Fatalf("join %d (%s): %d samples outside [%d, %d]", i, js[i].Class, n, lo, hi)
		}
		if js[i].Class == GapParagraph && n != 600*SampleRate/1000 {
			t.Fatalf("fixed 600ms gave %d samples", n)
		}
		if js[i].Class == GapList && n != 0 {
			t.Fatalf("fixed 0 gave %d samples", n)
		}
	}
	js = []Join{{CacheKey: "a", Class: GapPause, Pause: 1234567 * time.Microsecond}, {CacheKey: "b", Class: GapNone}}
	if gaps, err := GapSamples(g, js); err != nil || gaps[0] != samplesOf(1234567*time.Microsecond) {
		t.Fatalf("pause is exact: %v, %v", gaps, err)
	}
}

// accept: AUD-03#3
func TestGapDeterminism(t *testing.T) {
	g := DefaultGaps()
	js := keys(50, "d")
	a, err := GapSamples(g, js)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := GapSamples(g, js)
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatal("same input and seed gave different gaps")
	}
	// Edit chunk 20: only the joins either side of it (after 19 and after 20) may change.
	edited := append([]Join(nil), js...)
	edited[20].CacheKey = "edited"
	c, _ := GapSamples(g, edited)
	changed := 0
	for i := range a {
		if a[i] != c[i] {
			if i != 19 && i != 20 {
				t.Fatalf("editing chunk 20 changed the gap after chunk %d", i)
			}
			changed++
		}
	}
	if changed == 0 {
		t.Fatal("editing a chunk changed no neighbouring gap")
	}
	g.Seed = 1
	d, _ := GapSamples(g, js)
	same := 0
	for i := range a[:len(a)-1] {
		if a[i] == d[i] {
			same++
		}
	}
	if same > 3 {
		t.Fatalf("--gap-seed reshuffled only %d of %d gaps", len(a)-1-same, len(a)-1)
	}
}

// accept: AUD-03#4
func TestGapDrawsCoverRange(t *testing.T) {
	for _, r := range []GapRange{DefaultGaps().Sentence, DefaultGaps().Section, {0, 10 * time.Second}} {
		lo, hi := samplesOf(r.Min), samplesOf(r.Max)
		minN, maxN := hi, lo
		for i := range 1000 {
			n := DrawGap(0, fmt.Sprintf("before-%d", i), fmt.Sprintf("after-%d", i), GapSentence, r)
			minN, maxN = min(minN, n), max(maxN, n)
		}
		tol := float64(hi-lo) * 0.05
		if float64(minN-lo) > tol || float64(hi-maxN) > tol {
			t.Fatalf("range %s: draws span [%d, %d] of [%d, %d]", r, minN, maxN, lo, hi)
		}
	}
}

// accept: AUD-03#5
func TestInvalidGapRangesExit2(t *testing.T) {
	for _, s := range []string{"300ms..200ms", "-1ms", "-5ms..100ms", "11s", "1s..10.5s", "abc", "1s..", ""} {
		_, err := ParseGapRange(s)
		var ge *GapError
		if !errors.As(err, &ge) || ge.ExitCode() != exitcode.Usage {
			t.Errorf("%q: want a usage error (exit 2), got %v", s, err)
		}
	}
	for s, want := range map[string]GapRange{
		"0": {}, "600ms": {600 * time.Millisecond, 600 * time.Millisecond},
		"200ms..350ms": {200 * time.Millisecond, 350 * time.Millisecond}, "0..10s": {0, 10 * time.Second},
	} {
		if got, err := ParseGapRange(s); err != nil || got != want {
			t.Errorf("%q → %v, %v; want %v", s, got, err, want)
		}
	}
	g := DefaultGaps()
	g.Section = GapRange{2 * time.Second, time.Second}
	var ge *GapError
	if _, err := GapSamples(g, keys(3, "x")); !errors.As(err, &ge) {
		t.Errorf("invalid settings reached assembly: %v", err)
	}
}

// accept: AUD-03#7
func TestNoDiscontinuityAtJoins(t *testing.T) {
	const threshold = 200 // max step between neighbouring samples
	chunks := []PCM{dc(5000, 20000), dc(5000, -20000), dc(5000, 22000)}
	for _, gaps := range [][]int{{0, 0, 0}, {4800, 1200, 0}} {
		out := assemble(t, chunks, gaps, []int{0, 1, 2})
		prev := int16(0)
		for i := 0; i < len(out); i += BytesPerSample {
			v := int16(binary.LittleEndian.Uint16(out[i:]))
			if d := int(v) - int(prev); d > threshold || d < -threshold {
				t.Fatalf("gaps %v: step %d at sample %d", gaps, d, i/BytesPerSample)
			}
			prev = v
		}
		if prev != 0 {
			t.Fatalf("output does not end at zero: %d", prev)
		}
	}
	// The input is not modified by fading.
	if v := int16(binary.LittleEndian.Uint16(chunks[0])); v != 20000 {
		t.Fatalf("Fade mutated its input: %d", v)
	}
}

// accept: AUD-03#8
func TestOutOfOrderCompletionSameBytes(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	chunks := make([]PCM, 12)
	for i := range chunks {
		chunks[i] = dc(100+r.Intn(5000), int16(r.Intn(60000)-30000))
	}
	gaps, err := GapSamples(DefaultGaps(), keys(len(chunks), "o"))
	if err != nil {
		t.Fatal(err)
	}
	inOrder := make([]int, len(chunks))
	for i := range inOrder {
		inOrder[i] = i
	}
	want := assemble(t, chunks, gaps, inOrder)
	for range 20 {
		order := r.Perm(len(chunks))
		if got := assemble(t, chunks, gaps, order); !bytes.Equal(got, want) {
			t.Fatalf("order %v gave different bytes", order)
		}
	}
}

func TestAssemblerStreamsPrefixAndRejectsMisuse(t *testing.T) {
	var buf bytes.Buffer
	a := NewAssembler(&buf, []int{10, 0})
	if err := a.Add(1, dc(100, 5)); err != nil || buf.Len() != 0 {
		t.Fatalf("chunk 1 before 0 was written: %v, %d", err, buf.Len())
	}
	if err := a.Close(); err == nil {
		t.Fatal("incomplete assembly closed cleanly")
	}
	if err := a.Add(0, dc(100, 5)); err != nil || buf.Len() != (100+10+100)*BytesPerSample {
		t.Fatalf("prefix not streamed: %v, %d", err, buf.Len())
	}
	for _, i := range []int{0, 1, 2, -1} {
		if err := a.Add(i, dc(1, 0)); err == nil {
			t.Errorf("Add(%d) accepted", i)
		}
	}
	if err := NewAssembler(&buf, []int{0}).Add(0, PCM{1}); err == nil {
		t.Error("partial sample accepted")
	}
}

// Spans place each chunk at the sum of the chunks and gaps before it, whatever the arrival
// order: the timeline the manifest records.
// accept: OUT-02#3
func TestAssemblerSpans(t *testing.T) {
	chunks := []PCM{dc(100, 1000), dc(250, 1000), dc(40, 1000)}
	gaps := []int{30, 7, 0}
	var buf bytes.Buffer
	a := NewAssembler(&buf, gaps)
	for _, i := range []int{2, 0, 1} {
		if err := a.Add(i, chunks[i]); err != nil {
			t.Fatal(err)
		}
	}
	want := []Span{{0, 100}, {130, 380}, {387, 427}}
	got := a.Spans()
	if len(got) != len(want) {
		t.Fatalf("spans %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("spans %v, want %v", got, want)
		}
		if !bytes.Equal(buf.Bytes()[got[i].Start*BytesPerSample:got[i].End*BytesPerSample], Fade(chunks[i])) {
			t.Fatalf("chunk %d is not at its span", i)
		}
	}
	if int64(buf.Len()) != want[2].End*BytesPerSample {
		t.Fatalf("assembly %d bytes, last span ends at %d samples", buf.Len(), want[2].End)
	}
}
