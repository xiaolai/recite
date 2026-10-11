package recite_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeExec writes an executable shell script named name into dir.
func writeExec(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/usr/bin/env bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// runScript runs a repository script with bash, the fakes in fakeBin first on PATH, and the
// extra environment. It returns the exit code and the combined output.
func runScript(t *testing.T, fakeBin string, env []string, script string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Env = append(cmd.Env, env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err == nil {
		return 0, out.String()
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("running %s: %v", script, err)
	}
	return ee.ExitCode(), out.String()
}

// accept: REL-03#1
func TestTapVersionGuard(t *testing.T) {
	for _, tc := range []struct {
		current, next string
		want          int
	}{
		{"0.1.0", "0.2.0", 0},
		{"0.1.9", "0.1.10", 0}, // numeric, not lexical
		{"", "0.1.0", 0},       // no formula in the tap yet
		{"0.1.0", "v0.1.1", 0},
		{"0.2.0", "0.2.0", 1},
		{"0.2.0", "0.1.0", 1},
		{"0.1.10", "0.1.9", 1},
		{"0.1.0", "0.2.0-rc1", 1},
		{"", "0.2.0-rc1", 1},
		{"0.1.0", "latest", 1},
	} {
		code, out := runScript(t, t.TempDir(), nil, "scripts/tap-version-guard.sh", tc.current, tc.next)
		if code != tc.want {
			t.Errorf("guard(%q, %q) exit = %d, want %d: %s", tc.current, tc.next, code, tc.want, out)
		}
	}
}

// The formula's own test is what `brew test` runs after `brew install`: the installed
// binary's `recite version` must name the version the formula was rendered for.
// accept: REL-03#1
func TestFormulaTestChecksReleasedVersion(t *testing.T) {
	tmpl := readFile(t, "packaging/homebrew/recite.rb.tmpl")
	for _, want := range []string{
		`VERSION = "@VERSION@".freeze`,
		`assert_match "recite v#{VERSION}\n", shell_output("#{bin}/recite version")`,
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("formula template missing %q", want)
		}
	}

	dist := t.TempDir()
	var sums strings.Builder
	for _, p := range []string{"darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64"} {
		sums.WriteString(strings.Repeat("a", 64) + "  recite-" + p + ".tar.gz\n")
	}
	if err := os.WriteFile(filepath.Join(dist, "SHA256SUMS"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := runScript(t, t.TempDir(), []string{"DIST=" + dist}, "scripts/homebrew-formula.sh",
		"v1.2.3", "https://github.com/xiaolai/recite/releases/download", "https://github.com/xiaolai/recite")
	if code != 0 {
		t.Fatalf("homebrew-formula.sh exit %d: %s", code, out)
	}
	formula := readFile(t, filepath.Join(dist, "recite.rb"))
	if !strings.Contains(formula, `VERSION = "1.2.3".freeze`) {
		t.Errorf("rendered formula does not pin VERSION 1.2.3:\n%s", formula)
	}
	// publish-release.sh reads the tap's current version from this line for the guard.
	if !strings.Contains(readFile(t, "scripts/publish-release.sh"), `sed -n 's/^ *VERSION = "\([^"]*\)".*$/\1/p' "$formula"`) {
		t.Error("publish-release.sh no longer reads the tap's VERSION constant")
	}
}

// accept: REL-01#1
func TestReleaseBuildJobRunsEachNativeBinary(t *testing.T) {
	wf := readFile(t, ".github/workflows/check.yml")
	_, job, ok := strings.Cut(wf, "\n  release-build:\n")
	if !ok {
		t.Fatal("check.yml has no release-build job")
	}
	if next := strings.Index(job, "\n  fuzz-bench:"); next >= 0 {
		job = job[:next]
	}
	for _, want := range []string{
		"- runner: macos-15\n            binary: recite-darwin-arm64",
		"- runner: macos-15-intel\n            binary: recite-darwin-amd64",
		"- runner: ubuntu-24.04\n            binary: recite-linux-amd64",
		"- runner: ubuntu-24.04-arm\n            binary: recite-linux-arm64",
		"runs-on: ${{ matrix.runner }}",
		"- run: make release",
		`for b in recite-darwin-arm64 recite-darwin-amd64 recite-linux-amd64 recite-linux-arm64; do`,
		`test -s "dist/$b"`,
		`out="$("dist/$BINARY" version)"`,
		`grep -q "^commit: ${GITHUB_SHA}\$"`,
	} {
		if !strings.Contains(job, want) {
			t.Errorf("release-build job missing %q", want)
		}
	}
	if !strings.Contains(readFile(t, "scripts/build-release.sh"), "for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do") {
		t.Error("build-release.sh does not build all four targets")
	}
}

// accept: REL-02#3
func TestCleanMacWorkflowChecksGatekeeper(t *testing.T) {
	wf := readFile(t, ".github/workflows/clean-mac.yml")
	for _, want := range []string{
		"on:\n  workflow_dispatch:\n    inputs:\n      version:",
		"permissions:\n  contents: read",
		`[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]`,
		"VERSION: ${{ inputs.version }}",
		"- runner: macos-15\n            arch: arm64",
		"- runner: macos-15-intel\n            arch: amd64",
		`base="https://github.com/xiaolai/recite/releases/download/${VERSION}"`,
		`shasum -a 256 -c archive.sum`,
		`xattr -w com.apple.quarantine "0081;$(printf %x "$(date +%s)");Safari;$(uuidgen)" recite`,
		`out="$(./recite version)"`,
		`if [ "$first" != "recite ${VERSION}" ]; then`,
		"codesign --verify --strict --verbose=2 recite",
		`grep -Eq 'flags=0x[0-9a-f]+\(runtime\)' <<<"$sig"`,
		`assessment="$(spctl -a -vvv -t install recite 2>&1)"`,
		`grep -Fq 'source=Notarized Developer ID' <<<"$assessment"`,
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("clean-mac.yml missing %q", want)
		}
	}
	// A clean machine: nothing is checked out or built, and no secret reaches the job.
	for _, banned := range []string{"uses:", "secrets.", "make ", "go build", "GITHUB_TOKEN", "Authorization"} {
		if strings.Contains(wf, banned) {
			t.Errorf("clean-mac.yml contains %q", banned)
		}
	}
	// The input reaches shell only through the environment, never by expression expansion.
	for _, line := range strings.Split(wf, "\n") {
		if strings.Contains(line, "${{ inputs.") && !strings.HasPrefix(strings.TrimSpace(line), "VERSION: ") {
			t.Errorf("input expanded outside env: %q", strings.TrimSpace(line))
		}
	}
	if !strings.Contains(readFile(t, "scripts/publish-release.sh"), `gh workflow run clean-mac.yml --repo "$repo" --ref main -f version="$version"`) {
		t.Error("publish-release.sh does not dispatch clean-mac.yml")
	}
}

// signFakes installs fake codesign, ditto and xcrun into a new directory and a dist with
// both darwin binaries. flags is what `codesign --display` reports; status what notarytool
// info reports; verifyExit is the exit code of `codesign --verify`.
func signFakes(t *testing.T, flags, status string, verifyExit int) (bin, dist, log string) {
	t.Helper()
	bin, dist = t.TempDir(), t.TempDir()
	log = filepath.Join(t.TempDir(), "calls.log")
	writeExec(t, bin, "codesign", `echo "codesign $*" >>"$FAKE_LOG"
case " $* " in
*" --verify "*) exit "$FAKE_VERIFY_EXIT" ;;
*" --display "*|*" -dvvv "*)
	echo "Executable=$3" >&2
	echo "CodeDirectory v=20500 size=1 flags=$FAKE_FLAGS hashes=1+2 location=embedded" >&2
	echo "Authority=Developer ID Application: Test Person (TEAMID1234)" >&2 ;;
esac
`)
	writeExec(t, bin, "ditto", `echo "ditto $*" >>"$FAKE_LOG"
for last in "$@"; do :; done
: >"$last"
`)
	writeExec(t, bin, "xcrun", `echo "xcrun $*" >>"$FAKE_LOG"
case "$2" in
submit) echo "  id: 11111111-2222-3333-4444-555555555555"; echo "  status: $FAKE_STATUS" ;;
info) echo "  id: 11111111-2222-3333-4444-555555555555"; echo "  status: $FAKE_STATUS" ;;
esac
`)
	for _, arch := range []string{"arm64", "amd64"} {
		if err := os.WriteFile(filepath.Join(dist, "recite-darwin-"+arch), []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_FLAGS", flags)
	t.Setenv("FAKE_STATUS", status)
	t.Setenv("FAKE_VERIFY_EXIT", strconv.Itoa(verifyExit))
	return bin, dist, log
}

func runSign(t *testing.T, bin, dist string) (int, string) {
	t.Helper()
	return runScript(t, bin, []string{
		"DIST=" + dist,
		"RECITE_SIGN_IDENTITY=Developer ID Application: Test Person (TEAMID1234)",
		"NOTARY_PROFILE=test-profile",
	}, "scripts/sign-notarize.sh")
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// accept: REL-02#1
func TestSignNotarizeVerifiesHardenedSignature(t *testing.T) {
	bin, dist, log := signFakes(t, "0x10000(runtime)", "Accepted", 0)
	code, out := runSign(t, bin, dist)
	if code != 0 {
		t.Fatalf("sign-notarize.sh exit %d: %s", code, out)
	}
	calls := readLog(t, log)
	for _, arch := range []string{"arm64", "amd64"} {
		b := filepath.Join(dist, "recite-darwin-"+arch)
		for _, want := range []string{
			"codesign --force --sign Developer ID Application: Test Person (TEAMID1234) --options runtime --timestamp " + b,
			"codesign --verify --strict --verbose=2 " + b,
		} {
			if !strings.Contains(calls, want) {
				t.Errorf("no call %q in:\n%s", want, calls)
			}
		}
	}

	// Without the runtime flag the script stops before notarising anything.
	bin, dist, log = signFakes(t, "0x0(none)", "Accepted", 0)
	if code, out := runSign(t, bin, dist); code == 0 || !strings.Contains(out, "lacks the hardened runtime flag") {
		t.Errorf("missing runtime flag: exit %d: %s", code, out)
	}
	if calls := readLog(t, log); strings.Contains(calls, "xcrun") {
		t.Errorf("notarised a binary without the hardened runtime:\n%s", calls)
	}

	// A failing strict verification stops it too.
	bin, dist, _ = signFakes(t, "0x10000(runtime)", "Accepted", 1)
	if code, out := runSign(t, bin, dist); code == 0 {
		t.Errorf("failed codesign --verify: exit 0: %s", out)
	}
}

// accept: REL-02#2
func TestSignNotarizeRequiresAccepted(t *testing.T) {
	bin, dist, log := signFakes(t, "0x10000(runtime)", "Accepted", 0)
	if code, out := runSign(t, bin, dist); code != 0 {
		t.Fatalf("Accepted: exit %d: %s", code, out)
	}
	if calls := readLog(t, log); !strings.Contains(calls, "xcrun notarytool info 11111111-2222-3333-4444-555555555555 --keychain-profile test-profile") {
		t.Errorf("notarytool info not called with the submission id:\n%s", calls)
	}
	for _, arch := range []string{"arm64", "amd64"} {
		if info := readLog(t, filepath.Join(dist, "notary-info-darwin-"+arch+".txt")); !strings.Contains(info, "status: Accepted") {
			t.Errorf("notary-info-darwin-%s.txt = %q", arch, info)
		}
	}

	for _, status := range []string{"Invalid", "In Progress", "Rejected"} {
		bin, dist, _ := signFakes(t, "0x10000(runtime)", status, 0)
		if code, out := runSign(t, bin, dist); code == 0 || !strings.Contains(out, "is not Accepted") {
			t.Errorf("status %q: exit %d: %s", status, code, out)
		}
	}
}

// releaseCheckFakes installs a fake recite binary that fails the command named failOn (or
// none), and a fake go, so release-check.sh runs end to end without the network.
func releaseCheckFakes(t *testing.T, failOn string) (bin, dist, recite string) {
	t.Helper()
	bin, dist = t.TempDir(), t.TempDir()
	recite = filepath.Join(t.TempDir(), "recite")
	writeExec(t, filepath.Dir(recite), "recite", `if [ "$1" = version ]; then echo "recite v9.9.9"; echo "commit: abc"; exit 0; fi
if [ "$1" = "$FAKE_FAIL_ON" ]; then echo "fake failure" >&2; exit 4; fi
if [ "$1" = doctor ]; then echo "ok    auth"; fi
exit 0
`)
	writeExec(t, bin, "go", `echo "ok  fake $*"`+"\n")
	t.Setenv("FAKE_FAIL_ON", failOn)
	return bin, dist, recite
}

func latestReport(t *testing.T, dist string) string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dist, "release-check-*.md"))
	if err != nil || len(m) != 1 {
		t.Fatalf("want one report in %s, got %v (%v)", dist, m, err)
	}
	return readFile(t, m[0])
}

// accept: REL-05#1
func TestReleaseCheckExitsNonZeroOnAFailedStep(t *testing.T) {
	bin, dist, recite := releaseCheckFakes(t, "")
	env := []string{"RECITE_LIVE=1", "DIST=" + dist, "RECITE_BIN=" + recite}
	code, out := runScript(t, bin, env, "scripts/release-check.sh")
	if code != 0 {
		t.Fatalf("all steps passing: exit %d: %s", code, out)
	}
	report := latestReport(t, dist)
	for _, want := range []string{"- Version: recite v9.9.9\n", "\nResult: PASS ("} {
		if !strings.Contains(report, want) {
			t.Errorf("passing report missing %q:\n%s", want, report)
		}
	}

	for _, failOn := range []string{"doctor", "say"} {
		bin, dist, recite := releaseCheckFakes(t, failOn)
		env := []string{"RECITE_LIVE=1", "DIST=" + dist, "RECITE_BIN=" + recite}
		code, out := runScript(t, bin, env, "scripts/release-check.sh")
		if code != 1 {
			t.Errorf("%s failing: exit %d, want 1: %s", failOn, code, out)
		}
		if report := latestReport(t, dist); !strings.Contains(report, "\nResult: FAIL (") {
			t.Errorf("%s failing: report does not say FAIL:\n%s", failOn, report)
		}
	}

	if code, _ := runScript(t, bin, []string{"RECITE_LIVE=", "DIST=" + dist, "RECITE_BIN=" + recite}, "scripts/release-check.sh"); code != 2 {
		t.Errorf("without RECITE_LIVE=1: exit %d, want 2", code)
	}
}

// publishFakes makes publish-release.sh's local preconditions hold for v9.9.9 (fake git,
// signed darwin binaries, Accepted notarisations) and records every gh, curl and brew
// call, so a test can prove the script refused before any network write.
func publishFakes(t *testing.T, report, notaryStatus string) (bin, dist, log string) {
	t.Helper()
	bin, dist = t.TempDir(), t.TempDir()
	log = filepath.Join(t.TempDir(), "calls.log")
	const sha = "0123456789abcdef0123456789abcdef01234567"
	writeExec(t, bin, "git", `case "$1" in
rev-parse) echo `+sha+` ;;
ls-remote) printf '%s\trefs/tags/v9.9.9\n' `+sha+` ;;
status) ;;
*) echo "git $*" >>"$FAKE_LOG"; exit 1 ;;
esac
`)
	writeExec(t, bin, "codesign", `case " $* " in
*" -dvvv "*) echo "CodeDirectory v=20500 flags=0x10000(runtime) hashes=1" >&2
	echo "Authority=Developer ID Application: Test Person (TEAMID1234)" >&2 ;;
esac
`)
	for _, tool := range []string{"gh", "curl", "brew"} {
		writeExec(t, bin, tool, `echo "`+tool+` $*" >>"$FAKE_LOG"`+"\n")
	}
	for _, arch := range []string{"arm64", "amd64"} {
		if err := os.WriteFile(filepath.Join(dist, "recite-darwin-"+arch), []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dist, "notary-info-darwin-"+arch+".txt"), []byte("  status: "+notaryStatus+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if report != "" {
		if err := os.WriteFile(filepath.Join(dist, "release-check-20261012T000000Z.md"), []byte(report), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FAKE_LOG", log)
	return bin, dist, log
}

// accept: REL-05#1
func TestPublishReleaseRefusesWithoutPassingCheck(t *testing.T) {
	const pass = "# recite release check\n\n- Version: recite v9.9.9\n\nResult: PASS (10 steps)\n"
	for _, tc := range []struct {
		name, report, notary, want string
	}{
		{"no report", "", "Accepted", "no release-check report for v9.9.9"},
		{"other version", strings.Replace(pass, "v9.9.9", "v9.9.8", 1), "Accepted", "no release-check report for v9.9.9"},
		{"failed check", strings.Replace(pass, "Result: PASS (10 steps)", "Result: FAIL (1 of 10 steps failed)", 1), "Accepted", "did not pass"},
		{"not notarised", pass, "Invalid", "does not say Accepted"},
	} {
		bin, dist, log := publishFakes(t, tc.report, tc.notary)
		code, out := runScript(t, bin, []string{"DIST=" + dist}, "scripts/publish-release.sh", "v9.9.9")
		if code == 0 || !strings.Contains(out, tc.want) {
			t.Errorf("%s: exit %d, want a refusal containing %q: %s", tc.name, code, tc.want, out)
		}
		if calls := readLog(t, log); calls != "" {
			t.Errorf("%s: network commands ran before the refusal:\n%s", tc.name, calls)
		}
	}

	if code, out := runScript(t, t.TempDir(), nil, "scripts/publish-release.sh", "v9.9.9-rc1"); code == 0 {
		t.Errorf("prerelease version accepted: %s", out)
	}
}
