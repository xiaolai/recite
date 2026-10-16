package recite_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writeExec writes an executable shell script named name into dir.
func writeExec(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/usr/bin/env bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// fakeEnvFile holds, inside a fake bin directory, the settings its fakes read. They travel
// with the bin rather than through the process environment, so tests that use fakes can
// run in parallel.
const fakeEnvFile = ".fake-env.json"

// setFakeEnv sets key to value for every script runScript runs with the fakes in bin.
func setFakeEnv(t *testing.T, bin, key, value string) {
	t.Helper()
	vars := fakeEnv(t, bin)
	vars[key] = value
	b, err := json.Marshal(vars)
	must(t, err)
	writeFile(t, filepath.Join(bin, fakeEnvFile), string(b), 0o644)
}

// fakeEnv is the settings setFakeEnv stored for bin, none when there are none.
func fakeEnv(t *testing.T, bin string) map[string]string {
	t.Helper()
	vars := map[string]string{}
	b, err := os.ReadFile(filepath.Join(bin, fakeEnvFile))
	if os.IsNotExist(err) {
		return vars
	}
	must(t, err)
	must(t, json.Unmarshal(b, &vars))
	return vars
}

// runScript runs a repository script with bash, the fakes in fakeBin first on PATH, their
// settings, and the extra environment, which overrides both. It returns the exit code and
// the combined output.
func runScript(t *testing.T, fakeBin string, env []string, script string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for k, v := range fakeEnv(t, fakeBin) {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
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

// semverChain is the SemVer 2.0 specification's precedence example, lowest first.
var semverChain = []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
	"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0"}

// accept: REL-03#1
func TestTapVersionGuard(t *testing.T) {
	t.Parallel()
	type guardCase struct {
		current, next string
		want          int
		msg           string // what the refusal must say
	}
	cases := []guardCase{
		{"0.1.0", "0.2.0", 0, ""},
		{"0.1.9", "0.1.10", 0, ""}, // numeric, not lexical
		{"", "0.1.0", 0, ""},       // no formula in the tap yet
		{"0.1.0", "v0.1.1", 0, ""},
		{"v1.2.3", "1.2.4", 0, ""},
		{"1.9.9", "2.0.0", 0, ""},
		{"1.2.3", "1.10.0", 0, ""},
		// Wider than any shell integer: compared as digit strings, never overflowed.
		{"99999999999999999999.0.0", "100000000000000000000.0.0", 0, ""},
		{"100000000000000000000.0.0", "99999999999999999999.0.0", 1, "is not newer than"},
		{"0.2.0", "0.2.0", 1, "already has 0.2.0"},
		{"0.2.0", "0.1.0", 1, "is not newer than"},
		{"0.1.10", "0.1.9", 1, "is not newer than"},
		// NEW is stable only: the tap publishes releases, never prereleases or builds.
		{"0.1.0", "0.2.0-rc1", 1, "0.2.0-rc1 is not a canonical X.Y.Z release version"},
		{"", "0.2.0-rc1", 1, "is not a canonical X.Y.Z release version"},
		{"0.1.0", "0.2.0+build.1", 1, "0.2.0+build.1 is not a canonical X.Y.Z release version"},
		{"0.1.0", "latest", 1, "is not a canonical X.Y.Z release version"},
		{"", "01.2.3", 1, "01.2.3 is not a canonical X.Y.Z release version"},
		{"1.2.3", "1.02.4", 1, "1.02.4 is not a canonical X.Y.Z release version"},
		{"1.2.3", "1.2.03", 1, "is not a canonical X.Y.Z release version"},
		// CURRENT is any valid SemVer 2.0 version; anything else is never assumed older.
		{"0oops", "1.2.3", 1, "the tap's 0oops is not a valid SemVer version"},
		{"01.2.3", "1.2.4", 1, "the tap's 01.2.3 is not a valid SemVer version"},
		{"1.02.3", "1.2.4", 1, "is not a valid SemVer version"},
		{"1.2.3-", "1.2.4", 1, "is not a valid SemVer version"},
		{"1.2.3-01", "1.2.4", 1, "is not a valid SemVer version"}, // numeric identifier, leading zero
		{"1.2.3-rc.01", "1.2.4", 1, "is not a valid SemVer version"},
		{"1.2.3-rc..1", "1.2.4", 1, "is not a valid SemVer version"},
		{"1.2.3-rc_1", "1.2.4", 1, "is not a valid SemVer version"},
		{"1.2.3+", "1.2.4", 1, "is not a valid SemVer version"},
		{"1.2.3+a..b", "1.2.4", 1, "is not a valid SemVer version"},
		// A valid CURRENT prerelease is older than its release and everything after it.
		{"1.2.3-beta", "1.2.3", 0, ""},
		{"1.2.3-beta", "1.2.4", 0, ""},
		{"1.2.3-beta", "1.2.2", 1, "is not newer than"},
		{"1.2.3-0", "1.2.3", 0, ""},
		{"1.2.3-rc.1+build.5", "1.2.3", 0, ""},
		// Build metadata in CURRENT takes no part in precedence.
		{"1.2.3+build.5", "1.2.4", 0, ""},
		{"1.2.3+build.5", "1.2.3", 1, "is not newer than"},
		{"1.2.3+0a.01", "1.2.3", 1, "is not newer than"}, // build identifiers may have leading zeros
	}
	for _, v := range semverChain {
		cases = append(cases, guardCase{v, "0.9.9", 1, "is not newer than"}, guardCase{v, "1.0.1", 0, ""})
		if v == "1.0.0" {
			cases = append(cases, guardCase{v, "1.0.0", 1, "already has 1.0.0"})
		} else {
			cases = append(cases, guardCase{v, "1.0.0", 0, ""})
		}
	}
	for _, tc := range cases {
		code, out := runScript(t, t.TempDir(), nil, "scripts/tap-version-guard.sh", tc.current, tc.next)
		if code != tc.want || !strings.Contains(out, tc.msg) || (tc.want == 0 && out != "") {
			t.Errorf("guard(%q, %q) exit = %d, want %d with %q: %s", tc.current, tc.next, code, tc.want, tc.msg, out)
		}
	}
	if strings.Contains(readFile(t, "scripts/tap-version-guard.sh"), "sort -V") {
		t.Error("tap-version-guard.sh still orders versions with sort -V")
	}
}

// semverCmp runs the guard's comparator on two valid versions: -1, 0 or 1.
func semverCmp(t *testing.T, a, b string) string {
	t.Helper()
	cmd := exec.Command("bash", "-c", `source scripts/tap-version-guard.sh && semver_cmp "$1" "$2"`, "_", a, b)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("semver_cmp %s %s: %v: %s", a, b, err, out)
	}
	return strings.TrimSpace(string(out))
}

// The comparator orders every pair of the specification's example as SemVer 2.0 does,
// including the prerelease orderings a stable NEW never reaches through the guard.
// accept: REL-03#1
func TestTapVersionGuardSemverPrecedence(t *testing.T) {
	t.Parallel()
	for i, a := range semverChain {
		for j, b := range semverChain {
			want := "0"
			if i < j {
				want = "-1"
			} else if i > j {
				want = "1"
			}
			if got := semverCmp(t, a, b); got != want {
				t.Errorf("semver_cmp(%s, %s) = %s, want %s", a, b, got, want)
			}
		}
	}
	for _, tc := range []struct{ a, b, want string }{
		{"1.0.0-99999999999999999999", "1.0.0-100000000000000000000", "-1"}, // numeric, any length
		{"1.0.0-9", "1.0.0-10", "-1"},
		{"1.0.0-1", "1.0.0-a", "-1"},   // numeric before alphanumeric
		{"1.0.0-Z", "1.0.0-a", "-1"},   // ASCII order, not the locale's
		{"1.0.0-a", "1.0.0-a-b", "-1"}, // a hyphen is part of an alphanumeric identifier
		{"1.0.0-a.1", "1.0.0-a.1.0", "-1"},
		{"1.0.0-rc.1+b1", "1.0.0-rc.1+b2", "0"}, // build metadata is ignored
		{"1.0.0+b1", "1.0.0", "0"},
		{"2.0.0-alpha", "1.99.99", "1"},
	} {
		if got := semverCmp(t, tc.a, tc.b); got != tc.want {
			t.Errorf("semver_cmp(%s, %s) = %s, want %s", tc.a, tc.b, got, tc.want)
		}
		rev := map[string]string{"-1": "1", "0": "0", "1": "-1"}[tc.want]
		if got := semverCmp(t, tc.b, tc.a); got != rev {
			t.Errorf("semver_cmp(%s, %s) = %s, want %s", tc.b, tc.a, got, rev)
		}
	}
}

// The formula's own test is what `brew test` runs after `brew install`: the installed
// binary's `recite version` must name the version the formula was rendered for.
// accept: REL-03#1
func TestFormulaTestChecksReleasedVersion(t *testing.T) {
	t.Parallel()
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

// packageRelease runs package-release.sh on dist under the given time zone and umask, with
// TMPDIR at tmp and fakeBin first on the PATH.
func packageRelease(t *testing.T, dist, tmp, fakeBin, tz, umask string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", "-c", "umask "+umask+" && exec bash scripts/package-release.sh")
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DIST="+dist, "TMPDIR="+tmp, "TZ="+tz)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("running package-release.sh: %v", err)
	}
	return ee.ExitCode(), string(out)
}

// The archives are a function of the binaries' bytes alone: the local time zone, the umask,
// the files' own times and modes and any extended attributes change nothing, so a re-run
// reproduces a published release byte for byte. A failed run leaves no staging behind.
// accept: REL-03#1
func TestPackageReleaseIsReproducible(t *testing.T) {
	t.Parallel()
	platforms := []string{"darwin-arm64", "darwin-amd64", "linux-amd64", "linux-arm64"}
	newDist := func(mode os.FileMode, mtime time.Time) string {
		dist := t.TempDir()
		for _, p := range platforms {
			path := filepath.Join(dist, "recite-"+p)
			writeFile(t, path, "binary "+p, mode)
			must(t, os.Chmod(path, mode))
			must(t, os.Chtimes(path, mtime, mtime))
			// Extended attributes, where the host can set them, must not reach the archive.
			if xattr, err := exec.LookPath("xattr"); err == nil {
				if out, err := exec.Command(xattr, "-w", "com.example.recite-test", "stray", path).CombinedOutput(); err != nil {
					t.Fatalf("xattr: %v: %s", err, out)
				}
			}
		}
		return dist
	}
	distA, tmpA := newDist(0o700, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)), t.TempDir()
	distB, tmpB := newDist(0o775, time.Date(2019, 6, 7, 8, 9, 10, 0, time.UTC)), t.TempDir()
	if code, out := packageRelease(t, distA, tmpA, t.TempDir(), "Asia/Tokyo", "077"); code != 0 {
		t.Fatalf("package-release.sh: exit %d: %s", code, out)
	}
	if code, out := packageRelease(t, distB, tmpB, t.TempDir(), "America/Los_Angeles", "022"); code != 0 {
		t.Fatalf("package-release.sh: exit %d: %s", code, out)
	}
	for _, tmp := range []string{tmpA, tmpB} {
		if left, _ := os.ReadDir(tmp); len(left) != 0 {
			t.Errorf("staging left in TMPDIR: %v", left)
		}
	}
	epoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, p := range platforms {
		name := "recite-" + p + ".tar.gz"
		a, b := readFile(t, filepath.Join(distA, name)), readFile(t, filepath.Join(distB, name))
		if a != b {
			t.Errorf("%s differs between environments", name)
		}
		zr, err := gzip.NewReader(strings.NewReader(a))
		must(t, err)
		tr := tar.NewReader(zr)
		var names []string
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			must(t, err)
			names = append(names, h.Name)
			wantMode := int64(0o644)
			if h.Name == "recite" {
				wantMode = 0o755
			}
			if h.Typeflag != tar.TypeReg || h.Mode != wantMode || h.Uid != 0 || h.Gid != 0 ||
				h.Uname != "root" || h.Gname != "root" || !h.ModTime.Equal(epoch) || len(h.PAXRecords) != 0 || len(h.Xattrs) != 0 { //nolint:staticcheck // Xattrs is read to prove there are none
				t.Errorf("%s: %s header = type %c mode %o uid %d gid %d %s/%s mtime %v pax %v", name, h.Name,
					h.Typeflag, h.Mode, h.Uid, h.Gid, h.Uname, h.Gname, h.ModTime.UTC(), h.PAXRecords)
			}
		}
		if got := strings.Join(names, " "); got != "recite LICENSE README.md" {
			t.Errorf("%s holds %q, want recite LICENSE README.md", name, got)
		}
	}

	// A tar that fails mid-run still leaves no staging directory behind.
	fakeBin, tmp := t.TempDir(), t.TempDir()
	writeExec(t, fakeBin, "tar", `if [ "$1" = --version ]; then echo "bsdtar 3.5.3 - libarchive 3.7.4"; exit 0; fi
echo "tar: write error" >&2; exit 1
`)
	if code, out := packageRelease(t, newDist(0o755, epoch), tmp, fakeBin, "UTC", "022"); code == 0 {
		t.Errorf("failing tar: exit 0: %s", out)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("failed run left staging in TMPDIR: %v", left)
	}

	// An unknown tar cannot be told to drop extended attributes, so it is refused.
	writeExec(t, fakeBin, "tar", `echo "star 1.6"; exit 0
`)
	if code, out := packageRelease(t, newDist(0o755, epoch), t.TempDir(), fakeBin, "UTC", "022"); code == 0 ||
		!strings.Contains(out, "neither bsdtar nor GNU tar") {
		t.Errorf("unknown tar: exit %d: %s", code, out)
	}
}

// accept: REL-01#1
func TestReleaseBuildJobRunsEachNativeBinary(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
submit)
	# A concurrent re-sign while Apple processes the upload.
	if [ -n "${FAKE_MUTATE:-}" ]; then printf 'swapped while notarising' >"$FAKE_MUTATE"; fi
	echo "  id: 11111111-2222-3333-4444-555555555555"; echo "  status: $FAKE_STATUS" ;;
info) echo "  id: 11111111-2222-3333-4444-555555555555"; echo "  status: $FAKE_STATUS" ;;
esac
`)
	for _, arch := range []string{"arm64", "amd64"} {
		if err := os.WriteFile(filepath.Join(dist, "recite-darwin-"+arch), []byte("binary "+arch), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	setFakeEnv(t, bin, "FAKE_LOG", log)
	setFakeEnv(t, bin, "FAKE_FLAGS", flags)
	setFakeEnv(t, bin, "FAKE_STATUS", status)
	setFakeEnv(t, bin, "FAKE_VERIFY_EXIT", strconv.Itoa(verifyExit))
	setFakeEnv(t, bin, "FAKE_MUTATE", "")
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
	t.Parallel()
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
	t.Parallel()
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

// Apple notarises the bytes that were uploaded, so the recorded hash is the submitted
// snapshot's, and a release binary that changed during the wait is refused.
// accept: REL-02#2
func TestSignNotarizeBindsSubmittedBytes(t *testing.T) {
	t.Parallel()
	bin, dist, log := signFakes(t, "0x10000(runtime)", "Accepted", 0)
	if code, out := runSign(t, bin, dist); code != 0 {
		t.Fatalf("Accepted: exit %d: %s", code, out)
	}
	// ditto zips a staged copy, never the release binary itself.
	dittos := 0
	for _, line := range strings.Split(readLog(t, log), "\n") {
		if !strings.HasPrefix(line, "ditto\x1f") {
			continue
		}
		dittos++
		for _, arg := range strings.Split(line, "\x1f") {
			if arg == filepath.Join(dist, "recite-darwin-arm64") || arg == filepath.Join(dist, "recite-darwin-amd64") {
				t.Errorf("zipped the mutable release binary: %q", line)
			}
		}
	}
	if dittos != 2 {
		t.Errorf("ditto ran %d times, want 2", dittos)
	}

	bin, dist, _ = signFakes(t, "0x10000(runtime)", "Accepted", 0)
	setFakeEnv(t, bin, "FAKE_MUTATE", filepath.Join(dist, "recite-darwin-arm64"))
	code, out := runSign(t, bin, dist)
	if code == 0 || !strings.Contains(out, "changed while it was notarised") {
		t.Errorf("binary swapped during notarisation: exit %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dist, "notary-sha256-darwin-arm64.txt")); !os.IsNotExist(err) {
		t.Errorf("recorded a notarised hash for a swapped binary (%v)", err)
	}
}

// releaseCheckFakes installs a fake recite binary, a fake go and a fake ffprobe, so
// release-check.sh runs end to end without the network, and puts the four release binaries
// in dist. The recite fails the command named failOn (or none), and its say writes what
// sayMode names: "" a complete, fully verified sample whose audio is real WAV or MP4 bytes
// as its extension asks; any other mode an exit 0 whose evidence is broken in that one way
// (see the cases in TestReleaseCheckExitsNonZeroOnAFailedStep). The go records each call in
// goLog and fails a test run whose arguments contain goFail. The ffprobe fake reports on the
// bytes it is given, as the real tool does.
func releaseCheckFakes(t *testing.T, failOn, sayMode, goFail string) (bin, dist, recite, goLog string) {
	t.Helper()
	bin, dist = t.TempDir(), t.TempDir()
	recite = filepath.Join(t.TempDir(), "recite")
	goLog = filepath.Join(t.TempDir(), "go.log")
	writeExec(t, filepath.Dir(recite), "recite", `if [ "$1" = version ]; then echo "recite v9.9.9"; echo "commit: `+releaseRev+`"; exit 0; fi
if [ "$1" = "$FAKE_FAIL_ON" ]; then echo "fake failure" >&2; exit 4; fi
if [ "$1" = doctor ]; then echo "ok    auth"; exit 0; fi
if [ "$1" = say ]; then
	case " $* " in *" --verify normal "*) ;; *) echo "say without --verify normal" >&2; exit 8 ;; esac
	out="" prev=""
	for a in "$@"; do
		if [ "$prev" = -o ]; then out="$a"; fi
		prev="$a"
	done
	# 24 kHz mono s16le: a 44-byte header and 4 bytes (2 samples) of data.
	wav='RIFF\050\000\000\000WAVEfmt \020\000\000\000\001\000\001\000\300\135\000\000\200\273\000\000\002\000\020\000data\004\000\000\000'
	m4a='\000\000\000\030ftypM4A \000\000\000\000M4A mp42'
	case "$out" in *.m4a) kind=m4a ;; *) kind=wav ;; esac
	case "$FAKE_SAY" in
	noaudio) kind="" ;;
	mislabel) kind=wav ;;
	esac
	case "$kind" in
	wav)
		if [ "$FAKE_SAY" = truncwav ]; then
			printf "${wav}"'\001\002' >"$out" # the header promises 4 data bytes; 2 arrived
		else
			printf "${wav}"'\001\002\003\004' >"$out"
		fi ;;
	m4a)
		printf "$m4a" >"$out"
		if [ "$FAKE_SAY" = zerodur ]; then printf 'ZERO' >>"$out"; fi ;;
	esac
	# The summary fields, then the chunk records; a mode breaks one of them (chunk c2's for
	# the per-chunk modes).
	status=complete chunks=3 verified=3 unverified=0 noelig=0 pending=0 cached=0 renders=3 stt=3 records=3
	case "$FAKE_SAY" in
	nomanifest) exit 0 ;;
	malformed) printf '{"status":"complete","summary":{"chunks":3,' >"${out}.json"; exit 0 ;;
	unverified) verified=2 ;;
	nochunks) chunks=0 verified=0 renders=0 stt=0 records=0 ;;
	failed) status=failed ;;
	mismatch) records=2 ;;
	nostt) stt=0 ;;
	sumunverified) unverified=1 ;;
	sumnoeligible) noelig=1 ;;
	sumpending) pending=1 ;;
	sumcached) cached=1 ;;
	sumrenders) renders=4 ;;
	notacount) verified='"3"' ;;
	chunkstt0) stt=2 ;;
	sttmismatch) stt=4 ;;
	cachedchunk) cached=1 renders=2 ;;
	attempts0) renders=2 ;;
	esac
	recs=""
	for ((i = 1; i <= records; i++)); do
		state=verified ok=true from_cache=false attempts=1 final=1 calls=1
		takes='{"n":1,"status":"completed","eligible":true,"stt_scores":[0.98],"score":0.98,"rejection":null}'
		if [ "$i" = 2 ]; then
			case "$FAKE_SAY" in
			unverifiedrecord) state=unverified ok=false ;;
			chunkstt0) calls=0 takes='{"n":1,"status":"completed","eligible":true,"stt_scores":[],"score":0,"rejection":null}' ;;
			sttmismatch) calls=2 ;;
			cachedchunk) from_cache=true ;;
			attempts0) attempts=0 final=0 takes='' ;;
			attemptlog) takes='' ;;
			finalbad) final=2 ;;
			esac
		fi
		recs="${recs}${recs:+,}{\"id\":\"c${i}\",\"state\":\"${state}\",\"verified\":${ok},\"cached\":${from_cache},\"attempts\":${attempts},\"final_attempt\":${final},\"stt_calls\":${calls},\"attempt_log\":[${takes}]}"
	done
	printf '{"status":"%s","output":{"duration_s":1.5},"summary":{"chunks":%d,"verified":%s,"unverified":%d,"no_eligible":%d,"pending":%d,"cached":%d,"renders":%d,"stt_calls":%d},"chunks":[%s]}\n' \
		"$status" "$chunks" "$verified" "$unverified" "$noelig" "$pending" "$cached" "$renders" "$stt" "$recs" >"${out}.json"
fi
exit 0
`)
	// go version -m reads a fake release binary's "rev=R modified=M" as the build info the
	// real one prints for a Go binary, and refuses any other file as the real one does.
	writeExec(t, bin, "go", `if [ "$1 $2" = "version -m" ]; then
	body="$(cat -- "$3")"
	case "$body" in binary*) ;; *) echo "$3: not executable file" >&2; exit 1 ;; esac
	rev="$(sed -n 's/.* rev=\([^ ]*\).*/\1/p' <<<"$body")"
	mod="$(sed -n 's/.* modified=\([^ ]*\).*/\1/p' <<<"$body")"
	printf '%s: go1.27.2\n\tpath\tgithub.com/xiaolai/recite/cmd/recite\n\tbuild\t-trimpath=true\n\tbuild\tvcs=git\n' "$3"
	if [ -n "$rev" ]; then printf '\tbuild\tvcs.revision=%s\n' "$rev"; fi
	if [ -n "$mod" ]; then printf '\tbuild\tvcs.modified=%s\n' "$mod"; fi
	exit 0
fi
{ printf 'RECITE_BIN=%s' "${RECITE_BIN:-}"; printf '\x1f%s' "$@"; printf '\n'; } >>"$FAKE_GO_LOG"
if [ -n "$FAKE_GO_FAIL" ] && [[ " $* " == *"$FAKE_GO_FAIL"* ]]; then echo "--- FAIL: fake"; exit 1; fi
echo "ok  fake $*"
`)
	writeExec(t, bin, "ffprobe", fakeProbeHead+`case "$kind" in
wav) printf 'codec_name=pcm_s16le\nformat_name=wav\nduration=0.000083\n' ;;
mp4) printf 'codec_name=aac\nformat_name=mov,mp4,m4a,3gp,3g2,mj2\nduration=%s\n' "$dur" ;;
*) echo "$f: Invalid data found when processing input" >&2; exit 1 ;;
esac
`)
	for _, platform := range []string{"darwin-arm64", "darwin-amd64", "linux-amd64", "linux-arm64"} {
		writeFile(t, filepath.Join(dist, "recite-"+platform), fakeReleaseBinary(platform, releaseRev, "false"), 0o755)
	}
	setFakeEnv(t, bin, "FAKE_FAIL_ON", failOn)
	setFakeEnv(t, bin, "FAKE_SAY", sayMode)
	setFakeEnv(t, bin, "FAKE_GO_FAIL", goFail)
	setFakeEnv(t, bin, "FAKE_GO_LOG", goLog)
	return bin, dist, recite, goLog
}

// releaseRev is the commit the fake release binaries were built from.
const releaseRev = "abcdef0123456789abcdef0123456789abcdef01"

// fakeReleaseBinary is a fake release binary whose build info, as the fake go version -m
// reads it, names revision rev and vcs.modified modified ("" leaves either out).
func fakeReleaseBinary(platform, rev, modified string) string {
	s := "binary " + platform
	if rev != "" {
		s += " rev=" + rev
	}
	if modified != "" {
		s += " modified=" + modified
	}
	return s + "\n"
}

// fakeProbeHead starts a fake audio prober: it takes the last argument as the file (dropping
// ffprobe's file: prefix), and sets kind to wav or mp4 from the file's magic bytes and dur
// to its duration, N/A for a file marked ZERO.
const fakeProbeHead = `for f in "$@"; do :; done
f="${f#file:}"
kind=""
if [ "$(head -c 4 -- "$f")" = RIFF ]; then kind=wav; fi
if [ "$(head -c 8 -- "$f" | tail -c 4)" = ftyp ]; then kind=mp4; fi
dur=1.500
if grep -q ZERO -- "$f"; then dur=N/A; fi
`

// isolatedPath links the named host tools into bin and returns a PATH holding only bin, so
// a test controls exactly which optional tools (ffprobe, afinfo) a script can find.
func isolatedPath(t *testing.T, bin string, tools ...string) string {
	t.Helper()
	for _, tool := range tools {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("host tool %s: %v", tool, err)
		}
		must(t, os.Symlink(p, filepath.Join(bin, tool)))
	}
	return "PATH=" + bin
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
	t.Parallel()
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
		"- Commit: " + releaseRev + "\n",
		"- Binary sha256: " + sha256File(t, recite) + "\n",
		"- Binary: `~/",
		"Logs: `~/",
		"\nResult: PASS (",
		// The sample rows carry the numbers parsed from each manifest.
		"status=complete, chunks=3, verified=3, unverified=0, renders=3, stt_calls=3, duration_s=1.5 |",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("passing report missing %q:\n%s", want, report)
		}
	}
	// publish-release.sh binds the release to every binary this check saw, not only the one
	// it ran.
	for _, platform := range []string{"darwin-arm64", "darwin-amd64", "linux-amd64", "linux-arm64"} {
		for _, want := range []string{
			"- Release binary recite-" + platform + " sha256: " + sha256File(t, filepath.Join(dist, "recite-"+platform)) + "\n",
			"- Release binary recite-" + platform + " commit: " + releaseRev + "\n",
		} {
			if !strings.Contains(report, want) {
				t.Errorf("passing report missing %q:\n%s", want, report)
			}
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
		{"no manifest", "", "nomanifest", "", "no manifest"},
		{"malformed manifest", "", "malformed", "", "manifest is not valid JSON"},
		{"no chunks", "", "nochunks", "", "manifest has no chunks"},
		{"failed run", "", "failed", "", "manifest status is failed, not complete"},
		{"an unverified chunk record", "", "unverifiedrecord", "", "chunk c2 is not verified (state unverified)"},
		// Every summary count must be the one its chunk records give.
		{"fewer records than the summary", "", "mismatch", "", "manifest summary says chunks=3 but its chunk records give 2"},
		{"summary verified contradicts the records", "", "unverified", "", "manifest summary says verified=2 but its chunk records give 3"},
		{"summary unverified contradicts the records", "", "sumunverified", "", "manifest summary says unverified=1 but its chunk records give 0"},
		{"summary no_eligible contradicts the records", "", "sumnoeligible", "", "manifest summary says no_eligible=1 but its chunk records give 0"},
		{"summary pending contradicts the records", "", "sumpending", "", "manifest summary says pending=1 but its chunk records give 0"},
		{"summary cached contradicts the records", "", "sumcached", "", "manifest summary says cached=1 but its chunk records give 0"},
		{"summary renders contradicts the records", "", "sumrenders", "", "manifest summary says renders=4 but its chunk records give 3"},
		{"summary STT calls contradict the records", "", "nostt", "", "manifest summary says stt_calls=0 but its chunk records give 3"},
		{"a summary count that is not a number", "", "notacount", "", "manifest summary has no count for verified"},
		// Every chunk record must hold up on its own: rendered and transcribed by this run.
		{"a chunk without STT calls", "", "chunkstt0", "", "chunk c2 has no STT calls"},
		{"chunk STT calls contradict its takes", "", "sttmismatch", "", "chunk c2 counts 2 STT calls but its takes log 1 transcriptions"},
		{"a cached chunk", "", "cachedchunk", "", "chunk c2 was taken from the cache, not rendered and verified by this run"},
		{"a chunk without attempts", "", "attempts0", "", "chunk c2 has no attempts"},
		{"chunk attempts contradict its takes", "", "attemptlog", "", "chunk c2 counts 1 attempts but logs 0 takes"},
		{"a kept take that does not exist", "", "finalbad", "", "chunk c2 keeps take 2 of 1"},
		{"truncated WAV", "", "truncwav", "", "RIFF size 40 does not match the file's 46 bytes"},
		{"WAV bytes named m4a", "", "mislabel", "", "en.m4a is not an mp4 container (ffprobe: wav)"},
		{"m4a without audio", "", "zerodur", "", "en.m4a has no positive duration (ffprobe: N/A)"},
		{"round-trip test fails", "", "", "TestLiveSayHello", "--- FAIL: fake"},
		{"known-sentence test fails", "", "", "TestLiveHTTPKnownSentence", "--- FAIL: fake"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bin, dist, recite, _ := releaseCheckFakes(t, tc.failOn, tc.say, tc.goFail)
			env := []string{"RECITE_LIVE=1", "DIST=" + dist, "RECITE_BIN=" + recite}
			code, out := runScript(t, bin, env, "scripts/release-check.sh")
			if code != 1 || !strings.Contains(out, tc.want) {
				t.Errorf("exit %d, want 1 with %q: %s", code, tc.want, out)
			}
			if report := latestReport(t, dist); !strings.Contains(report, "\nResult: FAIL (") {
				t.Errorf("report does not say FAIL:\n%s", report)
			}
		})
	}

	if code, _ := runScript(t, bin, []string{"RECITE_LIVE=", "DIST=" + dist, "RECITE_BIN=" + recite}, "scripts/release-check.sh"); code != 2 {
		t.Errorf("without RECITE_LIVE=1: exit %d, want 2", code)
	}

	// Without all four release binaries there is nothing to bind the report to.
	bin, dist, recite, _ = releaseCheckFakes(t, "", "", "")
	must(t, os.Remove(filepath.Join(dist, "recite-linux-arm64")))
	env = []string{"RECITE_LIVE=1", "DIST=" + dist, "RECITE_BIN=" + recite}
	if code, out := runScript(t, bin, env, "scripts/release-check.sh"); code != 2 || !strings.Contains(out, "recite-linux-arm64 is missing") {
		t.Errorf("missing release binary: exit %d, want 2: %s", code, out)
	}

	// Every release binary must have been built, from a clean tree, of the commit the binary
	// the check runs reports; its build info is read without executing it.
	other := strings.Repeat("1", 40)
	for _, tc := range []struct {
		name, platform, content, want string
	}{
		{"a binary of another commit", "linux-arm64", fakeReleaseBinary("linux-arm64", other, "false"),
			"recite-linux-arm64 was built from " + other + ", not " + releaseRev},
		{"a binary of a modified tree", "darwin-amd64", fakeReleaseBinary("darwin-amd64", releaseRev, "true"),
			"recite-darwin-amd64 was built from a modified tree"},
		{"a binary without a revision", "linux-amd64", fakeReleaseBinary("linux-amd64", "", "false"),
			"recite-linux-amd64 records no vcs.revision"},
		{"a binary without vcs.modified", "linux-amd64", fakeReleaseBinary("linux-amd64", releaseRev, ""),
			"recite-linux-amd64 was built from a modified tree"},
		{"not a Go binary", "darwin-arm64", "#!/bin/sh\n", "go version -m cannot read recite-darwin-arm64"},
	} {
		bin, dist, recite, _ := releaseCheckFakes(t, "", "", "")
		writeFile(t, filepath.Join(dist, "recite-"+tc.platform), tc.content, 0o755)
		env := []string{"RECITE_LIVE=1", "DIST=" + dist, "RECITE_BIN=" + recite}
		code, out := runScript(t, bin, env, "scripts/release-check.sh")
		report := latestReport(t, dist)
		if code != 1 || !strings.Contains(out, tc.want) || !strings.Contains(report, "\nResult: FAIL (") {
			t.Errorf("%s: exit %d, want 1 with %q and a FAIL report: %s\n%s", tc.name, code, tc.want, out, report)
		}
	}
}

// Without ffprobe, the m4a is probed with afinfo; with neither, it cannot be shown to be
// audio and the step fails rather than passing on its size.
// accept: REL-05#1
func TestReleaseCheckProbesM4AWithoutFFprobe(t *testing.T) {
	t.Parallel()
	hostTools := []string{"bash", "env", "dirname", "basename", "date", "mkdir", "cat", "shasum", "awk",
		"head", "sed", "tail", "tr", "grep", "python3"}
	for _, tc := range []struct {
		name, say string
		afinfo    bool
		code      int
		want      string
	}{
		{"afinfo, good m4a", "", true, 0, "Result: PASS"},
		{"afinfo, WAV bytes named m4a", "mislabel", true, 1, "en.m4a is not an m4af file (afinfo: WAVE)"},
		{"afinfo, no duration", "zerodur", true, 1, "en.m4a has no positive duration (afinfo: 0.000)"},
		{"no prober", "", false, 1, "en.m4a cannot be probed: neither ffprobe nor afinfo is on the PATH"},
	} {
		bin, dist, recite, _ := releaseCheckFakes(t, "", tc.say, "")
		must(t, os.Remove(filepath.Join(bin, "ffprobe")))
		if tc.afinfo {
			writeExec(t, bin, "afinfo", fakeProbeHead+`case "$kind" in
wav) printf 'File:           %s\nFile type ID:   WAVE\nData format:     1 ch,  24000 Hz, lpcm (0x0000000C) 16 bits/channel\nestimated duration: 0.000083 sec\n' "$f" ;;
mp4) [ "$dur" = N/A ] && dur=0.000
	printf 'File:           %s\nFile type ID:   m4af\nData format:     1 ch,  24000 Hz, aac  (0x00000000) 0 bits/channel\nestimated duration: %s sec\n' "$f" "$dur" ;;
*) echo "AudioFileOpen failed ('typ?')" >&2; exit 1 ;;
esac
`)
		}
		env := []string{"RECITE_LIVE=1", "DIST=" + dist, "RECITE_BIN=" + recite, isolatedPath(t, bin, hostTools...)}
		code, out := runScript(t, bin, env, "scripts/release-check.sh")
		if code != tc.code || !strings.Contains(out+latestReport(t, dist), tc.want) {
			t.Errorf("%s: exit %d, want %d with %q: %s", tc.name, code, tc.code, tc.want, out)
		}
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

// passReport is a passing release-check report for v9.9.9 at publishHead that ran
// dist's darwin-arm64 binary and recorded the sha256 of all four release binaries in dist.
func passReport(t *testing.T, dist string) string {
	t.Helper()
	r := "# recite release check\n\n- Binary sha256: " + sha256File(t, filepath.Join(dist, "recite-darwin-arm64")) + "\n"
	for _, platform := range []string{"darwin-arm64", "darwin-amd64", "linux-amd64", "linux-arm64"} {
		r += "- Release binary recite-" + platform + " sha256: " + sha256File(t, filepath.Join(dist, "recite-"+platform)) + "\n"
		r += "- Release binary recite-" + platform + " commit: " + publishHead + "\n"
	}
	return r + "- Version: recite v9.9.9\n- Commit: " + publishHead + "\n\nResult: PASS (10 steps)\n"
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
status)
	# FAKE_GIT_STATUS is the porcelain output; FAKE_GIT_STATUS_EXIT a failure such as a
	# corrupt index, which prints nothing on stdout.
	if [ -n "$FAKE_GIT_STATUS_EXIT" ]; then echo "fatal: index file corrupt" >&2; exit "$FAKE_GIT_STATUS_EXIT"; fi
	printf '%s' "$FAKE_GIT_STATUS" ;;
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
		passReport(t, p.dist), 0o644)
	setFakeEnv(t, p.bin, "FAKE_LOG", p.log)
	setFakeEnv(t, p.bin, "FAKE_RELEASE", p.release)
	setFakeEnv(t, p.bin, "FAKE_TAG_REF", "tag "+publishTagObj)
	setFakeEnv(t, p.bin, "FAKE_TAG_COMMIT", publishHead)
	setFakeEnv(t, p.bin, "FAKE_FORMULA", "")
	setFakeEnv(t, p.bin, "FAKE_GIT_STATUS", "")
	setFakeEnv(t, p.bin, "FAKE_GIT_STATUS_EXIT", "")
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
	t.Parallel()
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
		{"report that ran another binary", func(t *testing.T, p publishEnv) {
			replaceIn(t, filepath.Join(p.dist, report), "- Binary sha256: "+sha256File(t, filepath.Join(p.dist, "recite-darwin-arm64")),
				"- Binary sha256: "+strings.Repeat("e", 64))
		}, "is not one of the release binaries"},
		{"report without a linux-arm64 hash", func(t *testing.T, p publishEnv) {
			replaceIn(t, filepath.Join(p.dist, report), "- Release binary recite-linux-arm64 sha256: ", "- Release binary recite-linux-arm64: ")
		}, "records no sha256 for recite-linux-arm64"},
		{"a binary built from another commit", func(t *testing.T, p publishEnv) {
			replaceIn(t, filepath.Join(p.dist, report), "- Release binary recite-linux-amd64 commit: "+publishHead,
				"- Release binary recite-linux-amd64 commit: "+strings.Repeat("1", 40))
		}, "recite-linux-amd64 was built from " + strings.Repeat("1", 40) + ", not HEAD " + publishHead},
		{"report without a darwin-amd64 commit", func(t *testing.T, p publishEnv) {
			replaceIn(t, filepath.Join(p.dist, report), "- Release binary recite-darwin-amd64 commit: ", "- Release binary recite-darwin-amd64: ")
		}, "records no commit for recite-darwin-amd64"},
		{"an archive whose binary is not the checked one", func(t *testing.T, p publishEnv) {
			// Packaging swaps linux-amd64's binary for other bytes just before tar reads it.
			realTar, err := exec.LookPath("tar")
			must(t, err)
			setFakeEnv(t, p.bin, "REAL_TAR", realTar)
			writeExec(t, p.bin, "tar", `dir="" prev="" create=""
for a in "$@"; do
	if [ "$prev" = -C ]; then dir="$a"; fi
	case "$a" in -cf) create=1 ;; esac
	prev="$a"
done
if [ -n "$create" ] && [ "${dir##*/}" = linux-amd64 ]; then printf 'swapped' >"$dir/recite"; fi
exec "$REAL_TAR" "$@"
`)
		}, "recite-linux-amd64.tar.gz holds a recite that is not the checked recite-linux-amd64"},
		{"dirty tree", func(t *testing.T, p publishEnv) { setFakeEnv(t, p.bin, "FAKE_GIT_STATUS", " M README.md\n") },
			"the working tree is not clean"},
		{"git status fails", func(t *testing.T, p publishEnv) { setFakeEnv(t, p.bin, "FAKE_GIT_STATUS_EXIT", "128") },
			"git status failed"},
		{"not notarised", func(t *testing.T, p publishEnv) {
			writeFile(t, filepath.Join(p.dist, "notary-info-darwin-amd64.txt"), "  status: Invalid\n", 0o644)
		}, "does not say Accepted"},
		{"no notarised hash", func(t *testing.T, p publishEnv) {
			must(t, os.Remove(filepath.Join(p.dist, "notary-sha256-darwin-arm64.txt")))
		}, "notary-sha256-darwin-arm64.txt is missing"},
		{"binary replaced after notarisation", func(t *testing.T, p publishEnv) {
			// Re-checked too, so only the notarisation record is stale.
			writeFile(t, filepath.Join(p.dist, "recite-darwin-amd64"), "re-signed later", 0o755)
			writeFile(t, filepath.Join(p.dist, report), passReport(t, p.dist), 0o644)
		}, "is not the binary that was notarised"},
		{"tag missing on xiaolai/recite", func(t *testing.T, p publishEnv) { setFakeEnv(t, p.bin, "FAKE_TAG_REF", "") },
			"tag v9.9.9 is not on xiaolai/recite"},
		{"tag on xiaolai/recite at another commit", func(t *testing.T, p publishEnv) { setFakeEnv(t, p.bin, "FAKE_TAG_COMMIT", "2222222") },
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

	// Each release binary is bound to the report on its own: replacing any one of them after
	// the check refuses, whichever platform it is.
	for _, platform := range []string{"darwin-arm64", "darwin-amd64", "linux-amd64", "linux-arm64"} {
		p := publishFakes(t)
		writeFile(t, filepath.Join(p.dist, "recite-"+platform), "rebuilt after the check", 0o755)
		code, out := p.run(t)
		if want := "recite-" + platform + " is not the binary " + filepath.Join(p.dist, report) + " checked"; code == 0 || !strings.Contains(out, want) {
			t.Errorf("%s replaced: exit %d, want a refusal containing %q: %s", platform, code, want, out)
		}
		if calls := readLog(t, p.log); calls != "" {
			t.Errorf("%s replaced: network commands ran before the refusal:\n%q", platform, calls)
		}
	}
}

// A version that is not a canonical vX.Y.Z is refused by the argument check itself, before
// any command runs: every tool the script could reach is a fake that records its call.
// accept: REL-05#1
func TestPublishReleaseRejectsNonReleaseVersions(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"v9.9.9-rc1", "v09.9.9", "v9.09.9", "v9.9.09", "9.9.9", "v9.9"} {
		bin := t.TempDir()
		log := filepath.Join(t.TempDir(), "calls.log")
		for _, tool := range []string{"git", "gh", "curl", "brew", "codesign", "shasum", "tar", "gzip"} {
			writeExec(t, bin, tool, logArgs+"exit 0\n")
		}
		code, out := runScript(t, bin, []string{"FAKE_LOG=" + log, "DIST=" + t.TempDir()}, "scripts/publish-release.sh", version)
		if want := "publish-release.sh: " + version + " is not a vX.Y.Z version\n"; code != 1 || out != want {
			t.Errorf("%s: exit %d, want 1 with exactly %q: %q", version, code, want, out)
		}
		if calls := readLog(t, log); calls != "" {
			t.Errorf("%s: commands ran before the version check:\n%q", version, calls)
		}
	}
}

// A re-run after a failure past the release step finishes the job without replacing
// anything, and refuses when what is already published is not this build.
// accept: REL-03#1
func TestPublishReleaseResumes(t *testing.T) {
	t.Parallel()
	p := publishFakes(t)
	// A lightweight tag names the commit directly.
	setFakeEnv(t, p.bin, "FAKE_TAG_REF", "commit "+publishHead)
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
	setFakeEnv(t, p.bin, "FAKE_FORMULA", tapFormula)
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

	// A published SHA256SUMS that is not this build's stops the run before the tap.
	sums := filepath.Join(p.release, "SHA256SUMS")
	goodSums := readFile(t, sums)
	writeFile(t, sums, strings.Repeat("0", 64)+"  recite-linux-amd64.tar.gz\n", 0o644)
	must(t, os.Remove(p.log))
	if code, out := p.run(t); code == 0 || !strings.Contains(out, "SHA256SUMS differs from this build's") {
		t.Errorf("differing SHA256SUMS: exit %d: %s", code, out)
	}
	if calls := readLog(t, p.log); strings.Contains(calls, "release\x1fcreate") || strings.Contains(calls, "repo\x1fclone") {
		t.Errorf("differing SHA256SUMS: continued or replaced:\n%q", calls)
	}
	writeFile(t, sums, goodSums, 0o644)

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

// A tap formula whose VERSION constant is a prerelease of this version is read whole and
// replaced by the release; one that is a later prerelease is not.
// accept: REL-03#1
func TestPublishReleaseReadsPrereleaseTapVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tapVersion string
		pushed     bool
		want       string
	}{
		{"9.9.9-rc.1", true, "tap updated to 9.9.9"},
		{"9.9.9-rc.1+build.7", true, "tap updated to 9.9.9"},
		{"10.0.0-alpha", false, "the tap has 10.0.0-alpha; refusing to replace it with 9.9.9"},
	} {
		p := publishFakes(t)
		tapFormula := filepath.Join(t.TempDir(), "recite.rb")
		writeFile(t, tapFormula, "class Recite < Formula\n  VERSION = \""+tc.tapVersion+"\"\n  version VERSION\nend\n", 0o644)
		setFakeEnv(t, p.bin, "FAKE_FORMULA", tapFormula)
		code, out := p.run(t)
		pushed := strings.Contains(readLog(t, p.log), "\x1fpush\x1forigin\x1fHEAD:main\n")
		if (code == 0) != tc.pushed || pushed != tc.pushed || !strings.Contains(out, tc.want) {
			t.Errorf("tap at %s: exit %d, pushed %v, want pushed %v with %q: %s", tc.tapVersion, code, pushed, tc.pushed, tc.want, out)
		}
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
