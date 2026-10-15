package auth

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/xiaolai/sayso/go/codexauth"

	"github.com/xiaolai/recite/internal/openai"
)

// Eight requests refused with one token cause one refresh, and all get its replacement. A
// refusal of the replacement refreshes again: there is no one-shot budget (D-4).
// accept: AUTH-04#1
func TestRefreshSingleFlight(t *testing.T) {
	st, _, _ := run(t, 0, "refresh-ok")
	wantState(t, st, StateReady)
	rec := readRecordEnv(t)
	stale, err := st.Creds.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	got := make([]Token, 8)
	errs := make([]error, 8)
	for i := range 8 {
		wg.Go(func() { got[i], errs[i] = st.Creds.RefreshAfter(context.Background(), stale) })
	}
	wg.Wait()
	if n := strings.Count(rec(), `"refreshToken":true`); n != 1 {
		t.Fatalf("%d refreshes, want 1", n)
	}
	for i := range 8 {
		if errs[i] != nil || got[i].Bearer() == stale.Bearer() {
			t.Fatalf("worker %d: err %v, stale token %v", i, errs[i], got[i].Bearer() == stale.Bearer())
		}
	}
	if _, err := st.Creds.RefreshAfter(context.Background(), got[0]); err != nil {
		t.Fatalf("second refusal: %v", err)
	}
	if n := strings.Count(rec(), `"refreshToken":true`); n != 2 {
		t.Fatalf("%d refreshes after a second refusal, want 2", n)
	}
}

// A refresh that leaves the token within the margin fails, and the next attempt waits for the
// backoff without starting Codex.
func TestRefreshWhenCloseToExpiry(t *testing.T) {
	st, _, _ := run(t, 0, "refresh-ok", "FAKECODEX_TOKEN_EXP="+soon(), "FAKECODEX_REFRESHED_EXP="+soon())
	wantState(t, st, StateExpired)
	rec := readRecordEnv(t)
	if n := strings.Count(rec(), `"refreshToken":true`); n != 1 {
		t.Fatalf("%d refreshes, want 1", n)
	}
	creds, err := codexauth.New(codexauth.Config{Source: codexauth.SourceCodex, Bin: fakeBin, CodexHome: t.TempDir(),
		Margin: RefreshMargin, Timeout: DefaultTimeout, Now: fixedNow, Guard: guard, Candidates: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := creds.Token(context.Background()); openai.ExitCode(err) != 3 {
			t.Fatalf("err %v, want exit 3", err)
		}
	}
	if n := strings.Count(rec(), `"refreshToken":true`); n != 2 {
		t.Fatalf("%d refreshes, want 2: the second Token call is inside the backoff", n)
	}
}

// accept: AUTH-04#2
func TestRefreshFailsExit3(t *testing.T) {
	st, _, _ := run(t, 0, "refresh-fails")
	wantState(t, st, StateReady)
	rec := readRecordEnv(t)
	stale, err := st.Creds.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Creds.RefreshAfter(context.Background(), stale)
	if openai.ExitCode(err) != 3 || codexauth.StateOf(err) != StateExpired {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(rec(), `"refreshToken":true`) {
		t.Fatal("refresh did not ask Codex with refreshToken: true")
	}
	// The file source cannot refresh.
	f := probeFile(t, homeWith(t, 1800000000), nil)
	wantState(t, f, StateReady)
	stale, err = f.Creds.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Creds.RefreshAfter(context.Background(), stale)
	if openai.ExitCode(err) != 3 || codexauth.StateOf(err) != StateExpired {
		t.Fatalf("file refresh: %v", err)
	}
}

// readRecordEnv reads the fake's record named by FAKECODEX_RECORD, as it stands when called.
func readRecordEnv(t *testing.T) func() string {
	path := strings.TrimSpace(getenvT(t, "FAKECODEX_RECORD"))
	return func() string { return readRecord(t, path) }
}

func getenvT(t *testing.T, k string) string {
	t.Helper()
	v, ok := os.LookupEnv(k)
	if !ok {
		t.Fatalf("%s is not set", k)
	}
	return v
}
