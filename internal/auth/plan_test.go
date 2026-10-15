package auth

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/usage"
)

// The fake app-server's account/rateLimits/read values render as the plan status, with no
// email or account ID.
// accept: OBS-04#1
func TestPlanStatusFromFakeAppServer(t *testing.T) {
	st, logs, _ := run(t, 10*time.Second, "chatgpt")
	wantState(t, st, StateReady)
	rl, ok, err := st.PlanStatus(context.Background())
	if err != nil || !ok {
		t.Fatalf("PlanStatus: ok %v err %v", ok, err)
	}
	got := usage.FormatPlan(rl)
	want := "Codex plan: pro\n" +
		"  primary window (7 d): 41% used, resets 2027-01-15 08:00 UTC\n" +
		"  ordinary usage allowed: yes\n"
	if got != want {
		t.Fatalf("plan status:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(readRecord(t, getenvT(t, "FAKECODEX_RECORD")), `"account/rateLimits/read"`) {
		t.Fatal("account/rateLimits/read was not called")
	}
	for _, leak := range []string{"canary@example.invalid", "acct-canary-0000", "eyJcanary"} {
		if strings.Contains(got, leak) || strings.Contains(logs.String(), leak) {
			t.Fatalf("%q leaked", leak)
		}
	}
}

// With the file source there is no app-server: the call is skipped with a warning.
// accept: OBS-04#1
func TestPlanStatusSkippedForFileSource(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	st := probeFile(t, homeWith(t, 1800000000), logger)
	wantState(t, st, StateReady)
	rl, ok, err := st.PlanStatus(context.Background())
	if err != nil || ok || len(rl.Windows) != 0 {
		t.Fatalf("file source: %+v %v %v", rl, ok, err)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "plan status skipped") {
		t.Fatalf("no warning: %q", logs.String())
	}
}

// A sign-in that is not ready is never asked for its plan: no child, no account method.
func TestPlanStatusSkippedWhenNotReady(t *testing.T) {
	st, _, _ := run(t, 10*time.Second, "apikey")
	wantState(t, st, StateAPIKey)
	before := readRecord(t, getenvT(t, "FAKECODEX_RECORD"))
	rl, ok, err := st.PlanStatus(context.Background())
	if err != nil || ok || len(rl.Windows) != 0 {
		t.Fatalf("not ready: %+v %v %v", rl, ok, err)
	}
	if after := readRecord(t, getenvT(t, "FAKECODEX_RECORD")); after != before {
		t.Fatal("PlanStatus started Codex for a sign-in that is not ready")
	}
}
