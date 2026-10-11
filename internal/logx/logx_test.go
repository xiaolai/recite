package logx

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

const opaque = "opaque-canary-123"

var canaries = []string{"eyJcanary.canary.canary", "ek_canary", opaque}

func init() { RegisterSecret(opaque) }

// noTime keeps log lines deterministic.
func noTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

func newTestLogger(buf *bytes.Buffer, json bool) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: noTime}
	if json {
		return slog.New(NewHandler(slog.NewJSONHandler(buf, opts)))
	}
	return slog.New(NewHandler(slog.NewTextHandler(buf, opts)))
}

func assertClean(t *testing.T, out string) {
	t.Helper()
	for _, c := range canaries {
		if strings.Contains(out, c) {
			t.Fatalf("canary %q leaked:\n%s", c, out)
		}
	}
}

type valuer struct{}

func (valuer) LogValue() slog.Value { return slog.StringValue("lv ek_canary") }

type stringer struct{}

func (stringer) String() string { return "str " + opaque }

// accept: AUTH-05#3
func TestHandlerScrubsMessageGroupsAndErrors(t *testing.T) {
	for _, json := range []bool{false, true} {
		var buf bytes.Buffer
		l := newTestLogger(&buf, json)
		wrapped := fmt.Errorf("mint failed: %w", errors.New("bad bearer eyJcanary.canary.canary"))
		l.Error("msg eyJcanary.canary.canary ek_canary "+opaque,
			slog.Group("outer", slog.Group("inner", slog.String("k", "x ek_canary y"), slog.Any("err", wrapped))),
			slog.Any("err", wrapped),
			slog.Any("lv", valuer{}),
			slog.Any("st", stringer{}),
			slog.Any("list", []string{opaque}),
		)
		l.With(slog.String("with", opaque)).WithGroup("g").Info("x", "k", opaque)
		out := buf.String()
		assertClean(t, out)
		if strings.Count(out, Redacted) < 9 {
			t.Errorf("expected redaction markers, got:\n%s", out)
		}
		if !strings.Contains(out, "mint failed") || !strings.Contains(out, "inner") {
			t.Errorf("non-secret context lost:\n%s", out)
		}
	}
}

// accept: AUTH-05#3
func TestHandlerDropsSecretKeys(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, false)
	l.Info("m", "Authorization", "Bearer plain", "TOKEN", "plain", "secret", "plain",
		slog.Group("g", slog.String("Value", "plain")), "keep", 1)
	out := buf.String()
	if strings.Contains(out, "plain") || !strings.Contains(out, "keep=1") {
		t.Fatalf("got %s", out)
	}
}

func TestScrubPatternsAndRegistry(t *testing.T) {
	RegisterSecret("")
	RegisterSecret(opaque + "-longer")
	cases := map[string]string{
		"a eyJx.y-z.w_1 b":               "a <redacted> b",
		"key=ek_ab-c_9":                  "key=<redacted>",
		opaque + "-longer and " + opaque: "<redacted> and <redacted>",
		"plain":                          "plain",
	}
	for in, want := range cases {
		if got := Scrub(in); got != want {
			t.Errorf("Scrub(%q) = %q, want %q", in, got, want)
		}
	}
}

// accept: OBS-01#1
func TestNetworkCallGoldenLine(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, false)
	LogCall(l, Call{
		Class: "ok", Path: "/v1/realtime/client_secrets?x=ek_canary", Status: 200,
		Latency: 412 * time.Millisecond, RequestID: "req_123", ChunkID: "c0001",
	})
	want := "level=INFO msg=\"network call\" class=ok path=/v1/realtime/client_secrets status=200 latency=412ms request_id=req_123 chunk_id=c0001\n"
	if got := buf.String(); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// accept: OBS-01#1
func TestNoHeaderOrBodyAtAnyLevel(t *testing.T) {
	for _, lvl := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		for _, json := range []bool{false, true} {
			var buf bytes.Buffer
			l := newTestLogger(&buf, json)
			body := `{"value":"ek_canary","session":{}}`
			l.Log(t.Context(), lvl, "response", "authorization", "Bearer "+opaque, "body", body,
				slog.Group("headers", slog.String("Authorization", "Bearer eyJcanary.canary.canary")))
			LogCall(l, Call{Class: "ok", Path: "/v1/realtime/client_secrets", Status: 200})
			out := buf.String()
			assertClean(t, out)
			if strings.Contains(strings.ToLower(out), "bearer") {
				t.Fatalf("header leaked at %v:\n%s", lvl, out)
			}
		}
	}
}

func TestLevel(t *testing.T) {
	cases := []struct {
		q    bool
		v    int
		want slog.Level
	}{{false, 0, slog.LevelWarn}, {false, 1, slog.LevelInfo}, {false, 2, slog.LevelDebug}, {false, 5, slog.LevelDebug}, {true, 2, slog.LevelError}}
	for _, c := range cases {
		if got := Level(c.q, c.v); got != c.want {
			t.Errorf("Level(%v,%d) = %v", c.q, c.v, got)
		}
	}
}

func TestNewJSONAndText(t *testing.T) {
	var a, b bytes.Buffer
	New(&a, slog.LevelInfo, true).Info("hi " + opaque)
	New(&b, slog.LevelInfo, false).Debug("hidden")
	if !strings.HasPrefix(a.String(), "{") || strings.Contains(a.String(), opaque) || b.Len() != 0 {
		t.Fatalf("json=%q text=%q", a.String(), b.String())
	}
}
