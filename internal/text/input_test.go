package text

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func wantInputErr(t *testing.T, err error, msg string, offset int64) {
	t.Helper()
	var e *InputError
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want *InputError", err)
	}
	if !strings.Contains(e.Error(), msg) || e.Offset != offset {
		t.Fatalf("err = %q (offset %d), want %q at offset %d", e.Error(), e.Offset, msg, offset)
	}
}

// accept: TXT-01#1
func TestExactlyOneSource(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "in.txt")
	if err := os.WriteFile(file, []byte("from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Read(Source{Args: []string{"hi"}, File: file}, 0)
	wantInputErr(t, err, "exactly one input source", -1)

	cases := []struct {
		name string
		src  Source
		want string
	}{
		{"text", Source{Args: []string{"Hello", "world"}}, "Hello world"},
		{"file", Source{File: file, Stdin: strings.NewReader("ignored")}, "from file"},
		{"dash arg", Source{Args: []string{"-"}, Stdin: strings.NewReader("piped"), StdinIsTTY: true}, "piped"},
		{"dash file", Source{File: "-", Stdin: strings.NewReader("piped")}, "piped"},
		{"implicit stdin", Source{Stdin: strings.NewReader("piped")}, "piped"},
	}
	for _, tc := range cases {
		got, err := Read(tc.src, 0)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q, %v", tc.name, got, err)
		}
	}
	// No source and a terminal on stdin: nothing to read.
	_, err = Read(Source{Stdin: strings.NewReader("x"), StdinIsTTY: true}, 0)
	wantInputErr(t, err, "no input", -1)
	_, err = Read(Source{File: filepath.Join(dir, "missing.txt")}, 0)
	wantInputErr(t, err, "cannot read input file", -1)
}

// accept: TXT-01#2
func TestInvalidUTF8ReportsOffset(t *testing.T) {
	_, err := Read(Source{Stdin: strings.NewReader("abc\xffdef")}, 0)
	wantInputErr(t, err, "not valid UTF-8 at byte offset 3", 3)
	// The offset counts the BOM: it refers to the input as given.
	_, err = Read(Source{Stdin: strings.NewReader("\uFEFFab\xc3")}, 0)
	wantInputErr(t, err, "not valid UTF-8", 5)
}

// accept: TXT-02#2
func TestNULReportsOffset(t *testing.T) {
	_, err := Read(Source{Args: []string{"hello\u0000world"}}, 0)
	wantInputErr(t, err, "NUL byte at byte offset 5", 5)
}

// accept: TXT-01#3
func TestNothingToSpeak(t *testing.T) {
	for _, in := range []string{"", "\uFEFF", " \r\n\t\n"} {
		_, err := Read(Source{Stdin: strings.NewReader(in)}, 0)
		if !errors.Is(err, ErrNothingToSpeak) || err.Error() != "nothing to speak" {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func TestMaxInput(t *testing.T) {
	if _, err := Read(Source{Stdin: strings.NewReader("12345")}, 4); err == nil || !strings.Contains(err.Error(), "--max-input") {
		t.Fatalf("over limit: %v", err)
	}
	if _, err := Read(Source{Args: []string{"12345"}}, 4); err == nil {
		t.Fatal("over limit text accepted")
	}
	if s, err := Read(Source{Stdin: strings.NewReader("1234")}, 4); err != nil || s != "1234" {
		t.Fatalf("at limit: %q %v", s, err)
	}
}

// M1 rejects control characters other than \n and \t until the guard lands (TXT-02).
// accept: TXT-02#1
func TestControlCharactersRejected(t *testing.T) {
	for _, tc := range []struct {
		in     string
		offset int64
	}{{"a\x07b", 1}, {"ab\rc", 2}, {"x\u0085y", 1}, {"\uFEFFa\x1b", 4}, {"a\r\r\nb", 1}} {
		_, err := Read(Source{Stdin: strings.NewReader(tc.in)}, 0)
		wantInputErr(t, err, "control character", tc.offset)
	}
	if s, err := Read(Source{Stdin: strings.NewReader("a\tb\r\nc\n")}, 0); err != nil || s != "a\tb\nc\n" {
		t.Fatalf("allowed controls: %q %v", s, err)
	}
}

// accept: TXT-02#1
func TestNormalizeGolden(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"e\u0301", "\u00e9"},                  // NFD → NFC
		{"\u212b", "\u00c5"},                   // angstrom sign → A with ring
		{"a\r\nb\r\n", "a\nb\n"},               // CRLF
		{"a\r\r\nb", "a\nb"},                   // CRCRLF reaches the fixpoint
		{"a\rb", "a\rb"},                       // lone CR is kept (the guard owns it)
		{"x\u200by\u202ez", "x\u200by\u202ez"}, // zero-width and bidi kept: nothing removed here
		{"“quoted” — dash", "“quoted” — dash"}, // smart quotes and dashes kept
		{"\ufb01", "\ufb01"},                   // NFC, not NFKC: the ligature stays
		{"\u1112\u1161\u11ab", "\ud55c"},       // conjoining jamo compose
	} {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// accept: TXT-02#1
func FuzzNormalize(f *testing.F) {
	for _, s := range []string{"", "e\u0301", "a\r\r\nb", "\r\r\r\n\n", "\u1100\u1161\u11a8", "\u0344\u0301", "x\r", "\r\n\u0301"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		once := Normalize(s)
		if twice := Normalize(once); twice != once {
			t.Fatalf("Normalize not idempotent on %q: %q then %q", s, once, twice)
		}
	})
}
