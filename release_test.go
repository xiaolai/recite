package recite_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
		{"0oops", "1.2.3", 1},      // an unreadable tap version is never assumed older
		{"1.2.3-beta", "1.2.4", 1}, // nor is a prerelease one
		{"v1.2.3", "1.2.4", 0},
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
	// A distinct sum per archive, so a sum rendered under the wrong platform shows.
	platforms := []string{"darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64"}
	sumOf := map[string]string{}
	var sums strings.Builder
	for i, p := range platforms {
		sumOf[p] = strings.Repeat(string(rune('a'+i)), 64)
		sums.WriteString(sumOf[p] + "  recite-" + p + ".tar.gz\n")
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
	for _, p := range platforms {
		pair := `url "https://github.com/xiaolai/recite/releases/download/v#{VERSION}/recite-` + p + `.tar.gz"` +
			"\n      sha256 \"" + sumOf[p] + "\""
		if !strings.Contains(formula, pair) {
			t.Errorf("rendered formula lacks %s with its own sha256:\n%s", p, formula)
		}
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

// logArgs is the first line of a fake: it appends the command and each argument to
// $FAKE_LOG, separated by \x1f, so a test sees the argument boundaries a real tool would.
const logArgs = `{ printf '%s' "$(basename "$0")"; printf '\x1f%s' "$@"; printf '\n'; } >>"$FAKE_LOG"` + "\n"

// argv is one call as logArgs records it.
func argv(args ...string) string {
	return strings.Join(args, "\x1f") + "\n"
}

// hasCall reports whether the log holds exactly this call on a line of its own.
func hasCall(calls string, args ...string) bool {
	return strings.Contains("\n"+calls, "\n"+argv(args...))
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(readFile(t, path)))
	return hex.EncodeToString(sum[:])
}

// signFakes installs fake codesign, ditto and xcrun into a new directory and a dist with
// both darwin binaries. flags is what `codesign --display` reports; status what notarytool
// info reports; verifyExit is the exit code of `codesign --verify`.
func signFakes(t *testing.T, flags, status string, verifyExit int) (bin, dist, log string) {
	t.Helper()
	bin, dist = t.TempDir(), t.TempDir()
	log = filepath.Join(t.TempDir(), "calls.log")
	writeExec(t, bin, "codesign", logArgs+`case " $* " in
*" --verify "*) exit "$FAKE_VERIFY_EXIT" ;;
*" --display "*|*" -dvvv "*)
	echo "Executable=$3" >&2
	echo "CodeDirectory v=20500 size=1 flags=$FAKE_FLAGS hashes=1+2 location=embedded" >&2
	echo "Authority=Developer ID Application: Test Person (TEAMID1234)" >&2 ;;
esac
`)
	writeExec(t, bin, "ditto", logArgs+`for last in "$@"; do :; done
: >"$last"
`)
	writeExec(t, bin, "xcrun", logArgs+`case "$2" in
submit) echo "  id: 11111111-2222-3333-4444-555555555555"; echo "  status: $FAKE_STATUS" ;;
info) echo "  id: 11111111-2222-3333-4444-555555555555"; echo "  status: $FAKE_STATUS" ;;
esac
`)
	for _, arch := range []string{"arm64", "amd64"} {
		if err := os.WriteFile(filepath.Join(dist, "recite-darwin-"+arch), []byte("binary "+arch), 0o755); err != nil {
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
		// The identity is one argument: split at its spaces, the real codesign fails.
		for _, want := range [][]string{
			{"codesign", "--force", "--sign", "Developer ID Application: Test Person (TEAMID1234)", "--options", "runtime", "--timestamp", b},
			{"codesign", "--verify", "--strict", "--verbose=2", b},
		} {
			if !hasCall(calls, want...) {
				t.Errorf("no call %q in:\n%q", want, calls)
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
	if calls := readLog(t, log); !hasCall(calls, "xcrun", "notarytool", "info", "11111111-2222-3333-4444-555555555555", "--keychain-profile", "test-profile") {
		t.Errorf("notarytool info not called with the submission id:\n%q", calls)
	}
	for _, arch := range []string{"arm64", "amd64"} {
		if info := readLog(t, filepath.Join(dist, "notary-info-darwin-"+arch+".txt")); !strings.Contains(info, "status: Accepted") {
			t.Errorf("notary-info-darwin-%s.txt = %q", arch, info)
		}
		// The Accepted record is bound to the bytes that were notarised.
		got := strings.TrimSpace(readLog(t, filepath.Join(dist, "notary-sha256-darwin-"+arch+".txt")))
		if want := sha256File(t, filepath.Join(dist, "recite-darwin-"+arch)); got != want {
			t.Errorf("notary-sha256-darwin-%s.txt = %q, want %q", arch, got, want)
		}
	}

	for _, status := range []string{"Invalid", "In Progress", "Rejected"} {
		bin, dist, _ := signFakes(t, "0x10000(runtime)", status, 0)
		if code, out := runSign(t, bin, dist); code == 0 || !strings.Contains(out, "is not Accepted") {
			t.Errorf("status %q: exit %d: %s", status, code, out)
		}
		if _, err := os.Stat(filepath.Join(dist, "notary-sha256-darwin-arm64.txt")); !os.IsNotExist(err) {
			t.Errorf("status %q: wrote a notarised hash (%v)", status, err)
		}
	}
}

// releaseCheckFakes installs a fake recite binary and a fake go, so release-check.sh runs
// end to end without the network. The recite fails the command named failOn (or none), and
// its say writes what sayMode names: "" a complete, fully verified sample; "noaudio",
// "nomanifest", "unverified", "nochunks" or "failed" an exit 0 without that evidence. The go
// records each call in goLog and fails a test run whose arguments contain goFail.
func releaseCheckFakes(t *testing.T, failOn, sayMode, goFail string) (bin, dist, recite, goLog string) {
	t.Helper()
	bin, dist = t.TempDir(), t.TempDir()
	recite = filepath.Join(t.TempDir(), "recite")
	goLog = filepath.Join(t.TempDir(), "go.log")
	writeExec(t, filepath.Dir(recite), "recite", `if [ "$1" = version ]; then echo "recite v9.9.9"; echo "commit: abc"; exit 0; fi
if [ "$1" = "$FAKE_FAIL_ON" ]; then echo "fake failure" >&2; exit 4; fi
if [ "$1" = doctor ]; then echo "ok    auth"; exit 0; fi
if [ "$1" = say ]; then
	case " $* " in *" --verify normal "*) ;; *) echo "say without --verify normal" >&2; exit 8 ;; esac
	out="" prev=""
	for a in "$@"; do
		if [ "$prev" = -o ]; then out="$a"; fi
		prev="$a"
	done
	status=complete chunks=3 verified=3
	case "$FAKE_SAY" in
	noaudio) ;;
	*) printf 'RIFF' >"$out" ;;
	esac
	case "$FAKE_SAY" in
	nomanifest) exit 0 ;;
	unverified) verified=2 ;;
	nochunks) chunks=0 verified=0 ;;
	failed) status=failed ;;
	esac
	printf '{"status":"%s","summary":{"chunks":%d,"verified":%d,"unverified":0,"stt_calls":3},"chunks":[{"status":"ok","verified":true}]}\n' \
		"$status" "$chunks" "$verified" >"${out}.json"
fi
exit 0
`)
	writeExec(t, bin, "go", `{ printf 'RECITE_BIN=%s' "${RECITE_BIN:-}"; printf '\x1f%s' "$@"; printf '\n'; } >>"$FAKE_GO_LOG"
if [ -n "$FAKE_GO_FAIL" ] && [[ " $* " == *"$FAKE_GO_FAIL"* ]]; then echo "--- FAIL: fake"; exit 1; fi
echo "ok  fake $*"
`)
	t.Setenv("FAKE_FAIL_ON", failOn)
	t.Setenv("FAKE_SAY", sayMode)
	t.Setenv("FAKE_GO_FAIL", goFail)
	t.Setenv("FAKE_GO_LOG", goLog)
	return bin, dist, recite, goLog
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
	bin, dist, recite, goLog := releaseCheckFakes(t, "", "", "")
	// HOME is the directory every temporary path is under, so the report must redact them all.
	home := filepath.Dir(filepath.Dir(recite))
	env := []string{"RECITE_LIVE=1", "DIST=" + dist, "RECITE_BIN=" + recite, "HOME=" + home}
	code, out := runScript(t, bin, env, "scripts/release-check.sh")
	if code != 0 {
		t.Fatalf("all steps passing: exit %d: %s", code, out)
	}
	report := latestReport(t, dist)
	for _, want := range []string{
		"- Version: recite v9.9.9\n",
		"- Commit: abc\n",
		"- Binary sha256: " + sha256File(t, recite) + "\n",
		"- Binary: `~/",
		"Logs: `~/",
		"\nResult: PASS (",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("passing report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, home) {
		t.Errorf("report names the home directory %s:\n%s", home, report)
	}
	// Both independent transcription runs happen, each exactly as the checklist names it.
	calls := readLog(t, goLog)
	for _, want := range [][]string{
		{"RECITE_BIN=" + recite, "test", "-tags", "live", "-count=1", "-run", "^TestLiveSayHello$", "-v", "./internal/cli"},
		{"RECITE_BIN=" + recite, "test", "-tags", "live", "-count=1", "-run", "^TestLiveHTTPKnownSentence$", "-v", "./internal/stt"},
	} {
		if !strings.Contains("\n"+calls, "\n"+argv(want...)) {
			t.Errorf("no go call %q in:\n%q", want, calls)
		}
	}

	for _, tc := range []struct {
		name, failOn, say, goFail, want string
	}{
		{"doctor fails", "doctor", "", "", "fake failure"},
		{"say fails", "say", "", "", "fake failure"},
		{"no audio", "", "noaudio", "", "no audio"},
		{"no manifest", "", "nomanifest", "", "manifest status is missing, not complete"},
		{"unverified chunk", "", "unverified", "", "verified 2 of 3 chunks"},
		{"no chunks", "", "nochunks", "", "manifest has no chunks"},
		{"failed run", "", "failed", "", "manifest status is failed, not complete"},
		{"round-trip test fails", "", "", "TestLiveSayHello", "--- FAIL: fake"},
		{"known-sentence test fails", "", "", "TestLiveHTTPKnownSentence", "--- FAIL: fake"},
	} {
		bin, dist, recite, _ := releaseCheckFakes(t, tc.failOn, tc.say, tc.goFail)
		env := []string{"RECITE_LIVE=1", "DIST=" + dist, "RECITE_BIN=" + recite}
		code, out := runScript(t, bin, env, "scripts/release-check.sh")
		if code != 1 || !strings.Contains(out, tc.want) {
			t.Errorf("%s: exit %d, want 1 with %q: %s", tc.name, code, tc.want, out)
		}
		if report := latestReport(t, dist); !strings.Contains(report, "\nResult: FAIL (") {
			t.Errorf("%s: report does not say FAIL:\n%s", tc.name, report)
		}
	}

	if code, _ := runScript(t, bin, []string{"RECITE_LIVE=", "DIST=" + dist, "RECITE_BIN=" + recite}, "scripts/release-check.sh"); code != 2 {
		t.Errorf("without RECITE_LIVE=1: exit %d, want 2", code)
	}
}

const (
	publishHead   = "0123456789abcdef0123456789abcdef01234567"
	publishTagObj = "fedcba9876543210fedcba9876543210fedcba98"
)

// publishEnv is a publish-release.sh run whose local preconditions hold for v9.9.9: the
// annotated tag on xiaolai/recite peels to HEAD, the four release binaries are in dist, the
// darwin ones signed and notarised for their current bytes, and a passing report checked
// darwin-arm64. A test breaks one precondition and expects a refusal.
type publishEnv struct {
	bin, dist, log string
	release        string // the fake GitHub release's assets
}

// passReport is a passing release-check report for v9.9.9 at publishHead that checked the
// binary with the given sha256.
func passReport(sha string) string {
	return "# recite release check\n\n- Binary sha256: " + sha + "\n- Version: recite v9.9.9\n- Commit: " +
		publishHead + "\n\nResult: PASS (10 steps)\n"
}

// publishFakes installs fakes for every external command publish-release.sh runs. gh, curl,
// brew and the tap's git calls are recorded in log (gh api reads are not), so a test can
// prove the script refused before any network write. gh release create copies the assets
// into release, curl downloads them from there, and gh repo clone starts the tap with
// $FAKE_FORMULA as its formula when that is set.
func publishFakes(t *testing.T) publishEnv {
	t.Helper()
	p := publishEnv{bin: t.TempDir(), dist: t.TempDir(), release: t.TempDir()}
	p.log = filepath.Join(t.TempDir(), "calls.log")
	writeExec(t, p.bin, "git", `case "$1" in
rev-parse) echo `+publishHead+` ;;
status) ;;
-C) `+logArgs+` ;;
*) `+logArgs+` exit 1 ;;
esac
`)
	writeExec(t, p.bin, "gh", `if [ "$1" = api ]; then
	case "$2" in
	repos/xiaolai/recite/git/ref/tags/v9.9.9) [ -n "$FAKE_TAG_REF" ] || exit 1; echo "$FAKE_TAG_REF" ;;
	repos/xiaolai/recite/git/tags/`+publishTagObj+`) echo "commit $FAKE_TAG_COMMIT" ;;
	*) exit 1 ;;
	esac
	exit 0
fi
`+logArgs+`case "$1 $2" in
"release view") [ -n "$(ls -A "$FAKE_RELEASE")" ] ;;
"release create")
	prev=""
	for a in "$@"; do
		if [ -f "$a" ] && [ "$prev" != --notes-file ]; then cp -- "$a" "$FAKE_RELEASE/"; fi
		prev="$a"
	done ;;
"repo clone")
	mkdir -p -- "$4/Formula"
	if [ -n "$FAKE_FORMULA" ]; then cp -- "$FAKE_FORMULA" "$4/Formula/recite.rb"; fi ;;
esac
`)
	writeExec(t, p.bin, "curl", logArgs+`out="" prev=""
for a in "$@"; do
	if [ "$prev" = -o ]; then out="$a"; fi
	prev="$a"
	url="$a"
done
cp -- "$FAKE_RELEASE/${url##*/}" "$out" 2>/dev/null || exit 22
`)
	writeExec(t, p.bin, "brew", logArgs)
	writeExec(t, p.bin, "codesign", `case " $* " in
*" -dvvv "*) echo "CodeDirectory v=20500 flags=0x10000(runtime) hashes=1" >&2
	echo "Authority=Developer ID Application: Test Person (TEAMID1234)" >&2 ;;
esac
`)
	for _, platform := range []string{"darwin-arm64", "darwin-amd64", "linux-amd64", "linux-arm64"} {
		writeFile(t, filepath.Join(p.dist, "recite-"+platform), "binary "+platform, 0o755)
	}
	for _, arch := range []string{"arm64", "amd64"} {
		writeFile(t, filepath.Join(p.dist, "notary-info-darwin-"+arch+".txt"), "  status: Accepted\n", 0o644)
		writeFile(t, filepath.Join(p.dist, "notary-sha256-darwin-"+arch+".txt"),
			sha256File(t, filepath.Join(p.dist, "recite-darwin-"+arch))+"\n", 0o644)
	}
	writeFile(t, filepath.Join(p.dist, "release-check-20261012T000000Z.md"),
		passReport(sha256File(t, filepath.Join(p.dist, "recite-darwin-arm64"))), 0o644)
	t.Setenv("FAKE_LOG", p.log)
	t.Setenv("FAKE_RELEASE", p.release)
	t.Setenv("FAKE_TAG_REF", "tag "+publishTagObj)
	t.Setenv("FAKE_TAG_COMMIT", publishHead)
	t.Setenv("FAKE_FORMULA", "")
	return p
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func (p publishEnv) run(t *testing.T) (int, string) {
	t.Helper()
	return runScript(t, p.bin, []string{"DIST=" + p.dist}, "scripts/publish-release.sh", "v9.9.9")
}

// accept: REL-05#1
func TestPublishReleaseRefusesWithoutPassingCheck(t *testing.T) {
	report := "release-check-20261012T000000Z.md"
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, p publishEnv)
		want   string
	}{
		{"no report", func(t *testing.T, p publishEnv) { must(t, os.Remove(filepath.Join(p.dist, report))) },
			"no release-check report for v9.9.9"},
		{"other version", func(t *testing.T, p publishEnv) { replaceIn(t, filepath.Join(p.dist, report), "v9.9.9", "v9.9.8") },
			"no release-check report for v9.9.9"},
		{"failed check", func(t *testing.T, p publishEnv) {
			replaceIn(t, filepath.Join(p.dist, report), "Result: PASS (10 steps)", "Result: FAIL (1 of 10 steps failed)")
		}, "did not pass"},
		{"report of another commit", func(t *testing.T, p publishEnv) {
			replaceIn(t, filepath.Join(p.dist, report), "- Commit: "+publishHead, "- Commit: 1111111")
		}, "did not check a build of HEAD"},
		{"report without a binary hash", func(t *testing.T, p publishEnv) {
			replaceIn(t, filepath.Join(p.dist, report), "- Binary sha256: ", "- Binary: ")
		}, "records no binary sha256"},
		{"report of another binary", func(t *testing.T, p publishEnv) {
			writeFile(t, filepath.Join(p.dist, "recite-darwin-arm64"), "rebuilt after the check", 0o755)
		}, "is not one of the release binaries"},
		{"not notarised", func(t *testing.T, p publishEnv) {
			writeFile(t, filepath.Join(p.dist, "notary-info-darwin-amd64.txt"), "  status: Invalid\n", 0o644)
		}, "does not say Accepted"},
		{"no notarised hash", func(t *testing.T, p publishEnv) {
			must(t, os.Remove(filepath.Join(p.dist, "notary-sha256-darwin-arm64.txt")))
		}, "notary-sha256-darwin-arm64.txt is missing"},
		{"binary replaced after notarisation", func(t *testing.T, p publishEnv) {
			writeFile(t, filepath.Join(p.dist, "recite-darwin-amd64"), "re-signed later", 0o755)
		}, "is not the binary that was notarised"},
		{"tag missing on xiaolai/recite", func(t *testing.T, p publishEnv) { t.Setenv("FAKE_TAG_REF", "") },
			"tag v9.9.9 is not on xiaolai/recite"},
		{"tag on xiaolai/recite at another commit", func(t *testing.T, p publishEnv) { t.Setenv("FAKE_TAG_COMMIT", "2222222") },
			"xiaolai/recite's tag v9.9.9 is 2222222, not HEAD"},
	} {
		p := publishFakes(t)
		tc.mutate(t, p)
		code, out := p.run(t)
		if code == 0 || !strings.Contains(out, tc.want) {
			t.Errorf("%s: exit %d, want a refusal containing %q: %s", tc.name, code, tc.want, out)
		}
		if calls := readLog(t, p.log); calls != "" {
			t.Errorf("%s: network commands ran before the refusal:\n%q", tc.name, calls)
		}
	}

	if code, out := runScript(t, t.TempDir(), nil, "scripts/publish-release.sh", "v9.9.9-rc1"); code == 0 {
		t.Errorf("prerelease version accepted: %s", out)
	}
}

// A re-run after a failure past the release step finishes the job without replacing
// anything, and refuses when what is already published is not this build.
// accept: REL-03#1
func TestPublishReleaseResumes(t *testing.T) {
	p := publishFakes(t)
	// A lightweight tag names the commit directly.
	t.Setenv("FAKE_TAG_REF", "commit "+publishHead)
	if code, out := p.run(t); code != 0 {
		t.Fatalf("first run: exit %d: %s", code, out)
	}
	calls := readLog(t, p.log)
	for _, want := range []string{"gh\x1frelease\x1fcreate\x1f", "\x1fpush\x1forigin\x1fHEAD:main\n"} {
		if !strings.Contains(calls, want) {
			t.Errorf("first run: no call with %q in:\n%q", want, calls)
		}
	}
	if !hasCall(calls, "gh", "workflow", "run", "clean-mac.yml", "--repo", "xiaolai/recite", "--ref", "main", "-f", "version=v9.9.9") {
		t.Errorf("first run did not dispatch clean-mac.yml:\n%q", calls)
	}

	// Re-run: the release and the tap formula already exist and match this build.
	tapFormula := filepath.Join(t.TempDir(), "recite.rb")
	writeFile(t, tapFormula, readFile(t, filepath.Join(p.dist, "recite.rb")), 0o644)
	t.Setenv("FAKE_FORMULA", tapFormula)
	must(t, os.Remove(p.log))
	code, out := p.run(t)
	if code != 0 || !strings.Contains(out, "already exists") || !strings.Contains(out, "tap already has 9.9.9") {
		t.Fatalf("re-run: exit %d: %s", code, out)
	}
	calls = readLog(t, p.log)
	if strings.Contains(calls, "release\x1fcreate") || strings.Contains(calls, "\x1fpush\x1f") {
		t.Errorf("re-run wrote again:\n%q", calls)
	}
	if !hasCall(calls, "gh", "workflow", "run", "clean-mac.yml", "--repo", "xiaolai/recite", "--ref", "main", "-f", "version=v9.9.9") {
		t.Errorf("re-run did not dispatch clean-mac.yml:\n%q", calls)
	}

	// An existing tap formula whose version cannot be read is never replaced.
	writeFile(t, tapFormula, "class Recite < Formula\n  version \"10.0.0\"\nend\n", 0o644)
	must(t, os.Remove(p.log))
	if code, out := p.run(t); code == 0 || !strings.Contains(out, "no readable VERSION") {
		t.Errorf("unreadable tap version: exit %d: %s", code, out)
	}
	if calls := readLog(t, p.log); strings.Contains(calls, "\x1fpush\x1f") || strings.Contains(calls, "workflow") {
		t.Errorf("unreadable tap version: continued:\n%q", calls)
	}

	// A published asset that differs from this build stops the run before the tap.
	writeFile(t, filepath.Join(p.release, "recite-linux-amd64.tar.gz"), "something else", 0o644)
	must(t, os.Remove(p.log))
	if code, out := p.run(t); code == 0 || !strings.Contains(out, "recite-linux-amd64.tar.gz differs from this build's") {
		t.Errorf("differing asset: exit %d: %s", code, out)
	}
	if calls := readLog(t, p.log); strings.Contains(calls, "release\x1fcreate") || strings.Contains(calls, "repo\x1fclone") {
		t.Errorf("differing asset: continued or replaced:\n%q", calls)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func replaceIn(t *testing.T, path, old, new string) {
	t.Helper()
	s := readFile(t, path)
	if !strings.Contains(s, old) {
		t.Fatalf("%s has no %q", path, old)
	}
	writeFile(t, path, strings.Replace(s, old, new, 1), 0o644)
}
