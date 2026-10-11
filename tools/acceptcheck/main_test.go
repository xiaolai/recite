package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func runFixture(t *testing.T, ledgerFile, dir string) (code int, stdout, stderr string) {
	t.Helper()
	d := filepath.Join("testdata", dir)
	var out, errb bytes.Buffer
	code = run(paths{
		ledger:   filepath.Join("testdata", ledgerFile),
		plan:     filepath.Join(d, "plan.md"),
		deferred: filepath.Join(d, "deferred.txt"),
		src:      filepath.Join(d, "src"),
	}, &out, &errb)
	return code, out.String(), errb.String()
}

// accept: FND-03#2
func TestAcceptcheck(t *testing.T) {
	tests := []struct {
		name, dir string
		code      int
		stdout    string   // exact, when code is 0
		stderr    []string // substrings, when code is not 0
	}{
		{
			name:   "all bullets of done features referenced",
			dir:    "pass",
			stdout: "acceptcheck: 2 done features, 4 bullets referenced, 0 deferred, OK\n",
		},
		{
			name:   "done feature with an unreferenced bullet",
			dir:    "missing",
			code:   1,
			stderr: []string{"FOO-01: done feature has no // accept: reference for FOO-01#2"},
		},
		{
			name:   "reference to a bullet that does not exist (nested and inline text not counted)",
			dir:    "nonexistent",
			code:   1,
			stderr: []string{"refs_test.go:5: reference to nonexistent bullet BAR-02#3"},
		},
		{
			name:   "deferral to an unknown work package",
			dir:    "deferral-unknown-wp",
			code:   1,
			stderr: []string{"deferred.txt:2: unknown work package WP-9.9"},
		},
		{
			name:   "deferral exempts while its work package is open",
			dir:    "deferral-active",
			stdout: "acceptcheck: 2 done features, 3 bullets referenced, 1 deferred, OK\n",
		},
		{
			name:   "deferral expires once its work package is DONE",
			dir:    "deferral-expired",
			code:   1,
			stderr: []string{"no // accept: reference for FOO-01#2"},
		},
		{
			name:   "feature claimed by two packages, only one DONE, is not done",
			dir:    "two-claimers",
			stdout: "acceptcheck: 1 done features, 2 bullets referenced, 0 deferred, OK\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runFixture(t, "ledger.md", tc.dir)
			if code != tc.code {
				t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", code, tc.code, stdout, stderr)
			}
			if tc.code == 0 && stdout != tc.stdout {
				t.Errorf("stdout = %q, want %q", stdout, tc.stdout)
			}
			for _, want := range tc.stderr {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr missing %q:\n%s", want, stderr)
				}
			}
		})
	}
}

// accept: FND-03#2
func TestAcceptcheckFeatureWithoutBulletsFails(t *testing.T) {
	code, _, stderr := runFixture(t, "ledger-empty.md", "pass")
	if code != 2 || !strings.Contains(stderr, "feature FOO-01 has no acceptance bullets") {
		t.Fatalf("exit = %d, stderr = %q; want 2 and a zero-bullet error", code, stderr)
	}
}

func TestLedgerBulletCounts(t *testing.T) {
	l, err := parseLedger(filepath.Join("testdata", "ledger.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"FOO-01": 2, "BAR-02": 2, "BAZ-03": 1}
	for id, n := range want {
		if l.bullets[id] != n {
			t.Errorf("%s: %d bullets, want %d", id, l.bullets[id], n)
		}
	}
}

// The real ledger, plan and deferral file must parse. Coverage itself is
// judged by `go run ./tools/acceptcheck` in make check, not here.
func TestRealRepository(t *testing.T) {
	var out, errb bytes.Buffer
	root := filepath.Join("..", "..")
	code := run(paths{
		ledger:   filepath.Join(root, ledgerPath),
		plan:     filepath.Join(root, planPath),
		deferred: filepath.Join(root, deferredPath),
		src:      root,
	}, &out, &errb)
	if code == 2 {
		t.Fatalf("real inputs do not parse:\n%s", errb.String())
	}
}
