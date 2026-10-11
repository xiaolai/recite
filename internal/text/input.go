package text

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// DefaultMaxInput is the input size limit unless --max-input raises it (TXT-01).
const DefaultMaxInput = 10 << 20

// InputError is a rejected input: always a usage/input failure (exit 2).
// Offset is the byte offset in the input as given, or -1 when none applies.
type InputError struct {
	Msg    string
	Offset int64
	Err    error
}

func (e *InputError) Error() string {
	s := e.Msg
	if e.Offset >= 0 {
		s = fmt.Sprintf("%s at byte offset %d", s, e.Offset)
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *InputError) Unwrap() error { return e.Err }

func inputErr(msg string, offset int64) error { return &InputError{Msg: msg, Offset: offset} }

// ErrNothingToSpeak is returned when the normalised input is empty or blank.
var ErrNothingToSpeak = &InputError{Msg: "nothing to speak", Offset: -1}

// Source is the raw input selection of `say`: positional text, -f FILE, or stdin.
type Source struct {
	Args       []string  // positional text; a lone "-" means stdin
	File       string    // -f/--file; "-" means stdin
	Stdin      io.Reader // nil means no stdin
	StdinIsTTY bool
}

// Read selects exactly one source, reads at most max bytes from it, decodes and
// normalises it, and applies the M1 control-character rule (TXT-01, TXT-02).
func Read(src Source, max int64) (string, error) {
	if max <= 0 {
		max = DefaultMaxInput
	}
	raw, err := readSource(src, max)
	if err != nil {
		return "", err
	}
	s, err := Decode(raw)
	if err != nil {
		return "", err
	}
	// Offsets refer to the input as given, so the control check runs before
	// normalisation; normalisation never introduces a control character.
	if err := CheckControl(s, int64(len(raw)-len(s))); err != nil {
		return "", err
	}
	s = Normalize(s)
	if strings.TrimSpace(s) == "" {
		return "", ErrNothingToSpeak
	}
	return s, nil
}

func readSource(src Source, max int64) ([]byte, error) {
	stdinArg := len(src.Args) == 1 && src.Args[0] == "-"
	n := 0
	if len(src.Args) > 0 {
		n++
	}
	if src.File != "" {
		n++
	}
	switch {
	case n > 1:
		return nil, inputErr("give exactly one input source: TEXT, -f FILE, or stdin (got text and -f)", -1)
	case src.File == "-" || stdinArg:
		return readStdin(src, max)
	case src.File != "":
		return readFile(src.File, max)
	case len(src.Args) > 0:
		s := strings.Join(src.Args, " ")
		if int64(len(s)) > max {
			return nil, tooLarge(max)
		}
		return []byte(s), nil
	case !src.StdinIsTTY && src.Stdin != nil:
		return readStdin(src, max)
	}
	return nil, inputErr("no input: give TEXT, -f FILE, or pipe text on stdin", -1)
}

func readStdin(src Source, max int64) ([]byte, error) {
	if src.Stdin == nil {
		return nil, inputErr("no stdin to read", -1)
	}
	return readLimited(src.Stdin, max, "stdin")
}

func readFile(path string, max int64) ([]byte, error) {
	f, err := os.Open(path) // read-only (design §14.2)
	if err != nil {
		return nil, &InputError{Msg: "cannot read input file", Offset: -1, Err: err}
	}
	defer func() { _ = f.Close() }()
	return readLimited(f, max, path)
}

func readLimited(r io.Reader, max int64, name string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, &InputError{Msg: "cannot read " + name, Offset: -1, Err: err}
	}
	if int64(len(b)) > max {
		return nil, tooLarge(max)
	}
	return b, nil
}

func tooLarge(max int64) error {
	return inputErr(fmt.Sprintf("input is larger than %d bytes; raise --max-input to allow it", max), -1)
}

var bom = []byte{0xEF, 0xBB, 0xBF}

// Decode strips a UTF-8 BOM and validates b: invalid UTF-8 and NUL bytes are
// rejected with their byte offset in b (TXT-01, G-ENCODING).
func Decode(b []byte) (string, error) {
	off := 0
	if bytes.HasPrefix(b, bom) {
		off = len(bom)
	}
	for i := off; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size <= 1 {
			return "", inputErr("input is not valid UTF-8", int64(i))
		}
		if r == 0 {
			return "", inputErr("input contains a NUL byte", int64(i))
		}
		i += size
	}
	return string(b[off:]), nil
}

// CheckControl is the M1 stand-in for the text guard: any control character
// other than \n, \t and the \r of a \r\n pair is rejected with its byte offset
// (base is added to offsets in s). WP-2.2 replaces it with G-CONTROL findings.
func CheckControl(s string, base int64) error {
	for i, r := range s {
		if r == '\n' || r == '\t' || (r == '\r' && strings.HasPrefix(s[i+1:], "\n")) {
			continue
		}
		if unicode.IsControl(r) {
			return inputErr(fmt.Sprintf("input contains control character U+%04X (supported from M2)", r), base+int64(i))
		}
	}
	return nil
}

// Normalize applies TXT-02: \r\n → \n, then NFC. Nothing else is removed.
// The newline rule runs to a fixpoint so normalising twice equals normalising
// once: a single pass turns "\r\r\n" into "\r\n", which a second pass would
// change again.
func Normalize(s string) string {
	for strings.Contains(s, "\r\n") {
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	return norm.NFC.String(s)
}

// IsInputError reports whether err is a rejected input (exit 2).
func IsInputError(err error) bool {
	var e *InputError
	return errors.As(err, &e)
}
