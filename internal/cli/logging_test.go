package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolai/sayso/go/secret"
)

func TestLoggingFromGlobals(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "recite.log")
	cases := []struct {
		args  []string
		level slog.Level
		json  bool
	}{
		{nil, slog.LevelWarn, false},
		{[]string{"-v"}, slog.LevelInfo, false},
		{[]string{"-vv"}, slog.LevelDebug, false},
		{[]string{"-q"}, slog.LevelError, false},
		{[]string{"-vv", "--log-file", logFile}, slog.LevelDebug, true},
	}
	for _, tc := range cases {
		var out, errb bytes.Buffer
		c := New(&out, &errb)
		var logger *slog.Logger
		c.Say = func(SayRequest) int {
			logger = c.Logger
			logger.Log(context.Background(), tc.level, "probe ek_canary")
			return ExitOK
		}
		if code := c.Run(append(tc.args, "say", "hi")); code != ExitOK {
			t.Fatalf("%v: exit %d: %s", tc.args, code, errb.String())
		}
		if logger.Enabled(context.Background(), tc.level-1) {
			t.Errorf("%v: level below %v enabled", tc.args, tc.level)
		}
		got := errb.String()
		if tc.json {
			b, err := os.ReadFile(logFile)
			if err != nil {
				t.Fatal(err)
			}
			got = string(b)
			if fi, _ := os.Stat(logFile); fi.Mode().Perm() != 0o600 {
				t.Errorf("log file mode %o", fi.Mode().Perm())
			}
			if !strings.HasPrefix(got, "{") {
				t.Errorf("want JSON, got %q", got)
			}
		}
		if !strings.Contains(got, "probe") || strings.Contains(got, "ek_canary") {
			t.Errorf("%v: log = %q", tc.args, got)
		}
	}
}

// accept: AUTH-05#3
func TestUserErrorsScrubbed(t *testing.T) {
	secret.Register("opaque-canary-cli")
	var errb bytes.Buffer
	c := New(&bytes.Buffer{}, &errb)
	err := fmt.Errorf("mint: %w", errors.New("bad opaque-canary-cli eyJcanary.canary.canary"))
	c.errorf("recite: %v", err)
	if s := errb.String(); strings.Contains(s, "canary") || !strings.Contains(s, "mint: bad") {
		t.Fatalf("stderr = %q", s)
	}
}
