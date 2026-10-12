#!/usr/bin/env bash
# The live release checklist (REL-05). It uses the real service and real usage, so it
# runs only with RECITE_LIVE=1. The release order (scripts/publish-release.sh,
# dev-docs/releasing.md): create the tag locally, make release, scripts/sign-notarize.sh,
# then this check, which must exit 0 before the tag is pushed and the release published.
# publish-release.sh attaches the report it writes to the release.
#
#   RECITE_LIVE=1 scripts/release-check.sh
#
#   DIST        the build directory the report and logs go to (default: dist); it must
#               hold the four release binaries, whose sha256 and commit the report records
#   RECITE_BIN  the binary to check (default: $DIST/recite-<GOOS>-<GOARCH> from make release)
#
# Steps: the release binaries' provenance (each built from the commit the checked binary
# reports, in a clean tree); doctor --deep; English, Chinese and Markdown samples; three effort levels; one
# compressed format (m4a); a transcription round-trip (the live tests that render with the
# built binary and transcribe the result independently). A sample passes only on its
# evidence: a manifest parsed as JSON (python3) whose chunk records are all verified and
# agree with its summary, with STT calls, and audio a decoder reads (ffprobe, else afinfo
# for the m4a).
set -euo pipefail

if [ "${RECITE_LIVE:-}" != "1" ]; then
	echo "release-check.sh: set RECITE_LIVE=1; this spends real usage against the service" >&2
	exit 2
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
dist="${DIST:-dist}"
bin="${RECITE_BIN:-${dist}/recite-$(go env GOOS)-$(go env GOARCH)}"
if [ ! -x "$bin" ]; then
	echo "release-check.sh: ${bin} is not an executable; run make release or set RECITE_BIN" >&2
	exit 2
fi
release_platforms=(darwin-arm64 darwin-amd64 linux-amd64 linux-arm64)
for platform in "${release_platforms[@]}"; do
	if [ ! -s "${dist}/recite-${platform}" ]; then
		echo "release-check.sh: ${dist}/recite-${platform} is missing; run make release and scripts/sign-notarize.sh first" >&2
		exit 2
	fi
done
# The round-trip test runs in its package directory, so it gets the binary by absolute path.
abs_bin="$(cd "$(dirname "$bin")" && pwd)/$(basename "$bin")"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
work="${dist}/release-check-${stamp}"
report="${dist}/release-check-${stamp}.md"
mkdir -p -- "$work"

cat >"${work}/en.txt" <<'TEXT'
The quick brown fox jumps over the lazy dog. On March 3, 2026, version 1.2 shipped to 4,096 users.
TEXT
cat >"${work}/zh.txt" <<'TEXT'
今天天气很好，我们去公园散步吧。这个版本在二〇二六年三月发布。
TEXT
cat >"${work}/sample.md" <<'TEXT'
# Release notes

Recite turns **text** into verified speech.

- It reads lists.
- It skips code blocks.

```sh
recite say hello
```
TEXT
cat >"${work}/short.txt" <<'TEXT'
Every effort level must read this sentence word for word.
TEXT

# redact replaces the repository root with . and the home directory with ~, so no row names
# the local account or the checkout's location.
redact() {
	local s="$1"
	s="${s//"$PWD"\//./}"
	# The replacement comes from an unquoted variable: bash 3.2 keeps a backslash in "\~" and
	# keeps quotes in a quoted replacement, and both would leak into the report.
	local tilde='~'
	printf '%s' "${s//"$HOME"/$tilde}"
}

# The report binds its result to these bytes and this commit: publish-release.sh refuses a
# report whose hash or commit is not that of what it publishes.
bin_sha="$(shasum -a 256 -- "$bin" | awk '{print $1}')"
version_out="$("$bin" version)"
run_commit="$(sed -n 's/^commit: //p' <<<"$version_out" | head -n 1)"
# Every release binary, not only the one run: publish-release.sh refuses to publish any of
# them whose bytes or commit differ from what this report records. Only the native binary
# can be run, so each one's commit is read from the build info the go command embeds
# (go version -m reads it without executing the binary, for any platform). That is
# vcs.revision, the checkout's HEAD, not the -X commit build-release.sh injects: -trimpath
# keeps -ldflags out of the build info, and the injected value is just a string in the
# binary. vcs.modified=false proves the build had no uncommitted change, and the run
# binary's own `version` must name the same commit, which ties the two together.
release_lines=""
provenance_log=""
for platform in "${release_platforms[@]}"; do
	f="${dist}/recite-${platform}"
	sha="$(shasum -a 256 -- "$f" | awk '{print $1}')"
	rev="" problem=""
	if ! info="$(go version -m "$f" 2>&1)"; then
		problem="go version -m cannot read recite-${platform}: $(redact "$(tr '\n' ' ' <<<"$info")")"
	else
		rev="$(sed -n 's/^[[:space:]]*build[[:space:]]*vcs\.revision=//p' <<<"$info")"
		mod="$(sed -n 's/^[[:space:]]*build[[:space:]]*vcs\.modified=//p' <<<"$info")"
		if [ -z "$rev" ] || [ "$(wc -l <<<"$rev")" -ne 1 ]; then
			problem="recite-${platform} records no vcs.revision"
			rev=""
		elif [ "$mod" != false ]; then
			problem="recite-${platform} was built from a modified tree (vcs.modified=${mod:-missing})"
		elif [ "$rev" != "$run_commit" ]; then
			problem="recite-${platform} was built from ${rev}, not ${run_commit:-an unknown commit}, the commit the checked binary reports"
		fi
	fi
	release_lines+="- Release binary recite-${platform} sha256: ${sha}"$'\n'
	release_lines+="- Release binary recite-${platform} commit: ${rev:-unknown}"$'\n'
	if [ -n "$problem" ]; then
		provenance_log+="FAIL ${problem}"$'\n'
	fi
done

# provenance prints the verdict on the release binaries' build info, failing on any problem.
provenance() {
	if [ -n "$provenance_log" ]; then
		printf '%s' "$provenance_log"
		return 1
	fi
	echo "ok all ${#release_platforms[@]} release binaries built from ${run_commit} in a clean tree"
}

failed=0
{
	echo "# recite release check"
	echo
	echo "- Started: ${stamp}"
	echo "- Binary: \`$(redact "$bin")\`"
	echo "- Binary sha256: ${bin_sha}"
	printf '%s' "$release_lines"
	echo "- Version: $(head -n 1 <<<"$version_out")"
	echo "- Commit: ${run_commit}"
	echo
	echo "| # | Step | Command | Exit | Seconds | Key numbers |"
	echo "|---|---|---|---|---|---|"
} >"$report"

n=0

# evidence_py MANIFEST AUDIO prints two lines: the manifest's key numbers for the report,
# then "ok" or the first reason the say step's output does not prove it passed. It parses
# the manifest as JSON and reconciles the summary with the chunk records, and checks a WAV's
# RIFF structure; it exits non-zero only if it cannot run, which the caller treats as a
# failure. Run with python3 -I, so nothing from the working tree is imported.
read -r -d '' evidence_py <<'PY' || true
import json, struct, sys

mpath, apath = sys.argv[1], sys.argv[2]

def done(numbers, verdict):
    # One line each, and nothing that splits the report's table row.
    clean = lambda s: s.replace("\n", " ").replace("|", "/")
    print(clean(numbers))
    print(clean(verdict))
    sys.exit(0)

def count(obj, key):
    v = obj.get(key) if isinstance(obj, dict) else None
    return v if type(v) is int else None  # bool is not a count

try:
    with open(mpath, "rb") as f:
        raw = f.read()
except OSError:
    raw = b""
if not raw:
    done("no manifest", "no manifest")
try:
    m = json.loads(raw)
except ValueError:
    done("unparseable manifest", "manifest is not valid JSON")
if not isinstance(m, dict):
    done("unparseable manifest", "manifest is not a JSON object")

status = m.get("status")
summary = m.get("summary")
records = m.get("chunks")
output = m.get("output")
duration = output.get("duration_s") if isinstance(output, dict) else None
chunks, verified = count(summary, "chunks"), count(summary, "verified")
stt_calls = count(summary, "stt_calls")
show = lambda v: "missing" if v is None else str(v)
numbers = ", ".join([
    "status=" + show(status),
    "chunks=" + show(chunks),
    "verified=" + show(verified),
    "unverified=" + show(count(summary, "unverified")),
    "renders=" + show(count(summary, "renders")),
    "stt_calls=" + show(stt_calls),
    "duration_s=" + show(duration),
])

if status != "complete":
    done(numbers, "manifest status is %s, not complete" % ("missing" if status is None else status))
if not isinstance(records, list) or not records or chunks is None or chunks <= 0:
    done(numbers, "manifest has no chunks")

# Each chunk record must stand on its own: verified, rendered by this run (the samples run
# with --no-cache), every take it counts logged, the kept take one of them, and at least
# one STT call, each of which logged a transcription score (manifest.go's fill: a call
# that fails ends the run, so a complete run logs every call it counts).
recount = {"chunks": len(records), "verified": 0, "unverified": 0, "no_eligible": 0,
           "pending": 0, "cached": 0, "renders": 0, "stt_calls": 0}
for i, r in enumerate(records):
    if not isinstance(r, dict):
        done(numbers, "chunk #%d is not a JSON object" % (i + 1))
    cid = r.get("id") if isinstance(r.get("id"), str) and r.get("id") else "#%d" % (i + 1)
    state = r.get("state")
    if state != "verified" or r.get("verified") is not True:
        done(numbers, "chunk %s is not verified (state %s)" % (cid, show(state)))
    if r.get("cached") is not False:
        done(numbers, "chunk %s was taken from the cache, not rendered and verified by this run" % cid)
    attempts, final, calls = count(r, "attempts"), count(r, "final_attempt"), count(r, "stt_calls")
    takes = r.get("attempt_log")
    if attempts is None or attempts <= 0:
        done(numbers, "chunk %s has no attempts" % cid)
    if not isinstance(takes, list) or len(takes) != attempts:
        done(numbers, "chunk %s counts %d attempts but logs %s takes" % (
            cid, attempts, len(takes) if isinstance(takes, list) else "no"))
    if final is None or not 1 <= final <= attempts:
        done(numbers, "chunk %s keeps take %s of %d" % (cid, show(final), attempts))
    if calls is None or calls <= 0:
        done(numbers, "chunk %s has no STT calls" % cid)
    scores = 0
    for t in takes:
        s = t.get("stt_scores") if isinstance(t, dict) else None
        if not isinstance(s, list):
            done(numbers, "chunk %s logs a take without transcription scores" % cid)
        scores += len(s)
    if scores != calls:
        done(numbers, "chunk %s counts %d STT calls but its takes log %d transcriptions" % (cid, calls, scores))
    recount["verified"] += 1
    recount["renders"] += attempts
    recount["stt_calls"] += calls

# Every summary count must be the one the chunk records give (manifest.go's count).
for key in ("chunks", "verified", "unverified", "no_eligible", "pending", "cached", "renders", "stt_calls"):
    v = count(summary, key)
    if v is None:
        done(numbers, "manifest summary has no count for %s" % key)
    if v != recount[key]:
        done(numbers, "manifest summary says %s=%d but its chunk records give %d" % (key, v, recount[key]))

if apath.endswith(".wav"):
    try:
        with open(apath, "rb") as f:
            wav = f.read()
    except OSError:
        done(numbers, "no audio")
    name = apath.rsplit("/", 1)[-1]
    if len(wav) < 12 or wav[:4] != b"RIFF" or wav[8:12] != b"WAVE":
        done(numbers, "%s is not a RIFF/WAVE file" % name)
    riff = struct.unpack_from("<I", wav, 4)[0]
    if riff + 8 != len(wav):
        done(numbers, "%s: RIFF size %d does not match the file's %d bytes" % (name, riff, len(wav)))
    fmt, data, at = None, None, 12
    while at + 8 <= len(wav):
        cid, size = wav[at:at + 4], struct.unpack_from("<I", wav, at + 4)[0]
        if at + 8 + size > len(wav):
            done(numbers, "%s: the %s chunk claims %d bytes past the end of the file" % (name, cid.decode("latin-1"), size))
        if cid == b"fmt " and size >= 16:
            fmt = struct.unpack_from("<HHIIHH", wav, at + 8)
        elif cid == b"data":
            data = size
        at += 8 + size + (size & 1)
    if fmt is None or fmt[0] != 1 or fmt[1] == 0 or fmt[2] == 0 or fmt[4] == 0:
        done(numbers, "%s has no PCM fmt chunk" % name)
    if not data or data % fmt[4] != 0:
        done(numbers, "%s has no whole samples of audio" % name)
done(numbers, "ok")
PY

# say_evidence MANIFEST AUDIO prints the manifest's numbers on its first line, and on its
# second "ok" or why the step's output does not prove it passed.
say_evidence() {
	local m="$1" audio="$2" out bad
	if ! out="$(python3 -I -c "$evidence_py" "$m" "$audio" 2>&1)"; then
		printf 'unchecked manifest\nthe manifest check could not run: %s' "$(tr '\n' ' ' <<<"$out")"
		return
	fi
	bad="$(sed -n 2p <<<"$out")"
	if [ ! -s "$audio" ]; then
		bad='no audio'
	elif [ "$bad" = ok ]; then
		bad="$(probe_audio "$audio")"
		bad="${bad:-ok}"
	fi
	printf '%s\n%s' "$(head -n 1 <<<"$out")" "$bad"
}

# probe_audio AUDIO prints why a decoder does not see AUDIO as the audio its extension names,
# or nothing: the container, the codec and a positive duration. ffprobe (installed with the
# ffmpeg recite encodes with) is used where it exists; afinfo, which reads the m4a that
# recite's afconvert fallback writes on macOS, otherwise. A WAV without either is still
# checked structurally by evidence_py; an m4a without either cannot be shown to be audio.
probe_audio() {
	local audio="$1" name info container codec duration want_container want_codec
	name="$(basename "$audio")"
	case "$audio" in
	*.wav) want_container=wav want_codec=pcm_s16le ;;
	*.m4a) want_container=mp4 want_codec=aac ;;
	*)
		printf '%s: no probe rule for this format' "$name"
		return
		;;
	esac
	if command -v ffprobe >/dev/null 2>&1; then
		if ! info="$(ffprobe -v error -select_streams a:0 -show_entries format=format_name,duration:stream=codec_name \
			-of default=noprint_wrappers=1 "file:${audio}" 2>&1)"; then
			printf '%s cannot be read by ffprobe: %s' "$name" "$(tr '\n' ' ' <<<"$info")"
			return
		fi
		container="$(sed -n 's/^format_name=//p' <<<"$info" | head -n 1)"
		codec="$(sed -n 's/^codec_name=//p' <<<"$info" | head -n 1)"
		duration="$(sed -n 's/^duration=//p' <<<"$info" | head -n 1)"
		if [[ ",${container}," != *",${want_container},"* ]]; then
			printf '%s is not an %s container (ffprobe: %s)' "$name" "$want_container" "${container:-none}"
		elif [ "$codec" != "$want_codec" ]; then
			printf '%s is not %s audio (ffprobe: %s)' "$name" "$want_codec" "${codec:-no audio stream}"
		elif ! positive "$duration"; then
			printf '%s has no positive duration (ffprobe: %s)' "$name" "${duration:-none}"
		fi
	elif [ "$want_container" = mp4 ] && command -v afinfo >/dev/null 2>&1; then
		if ! info="$(afinfo "$audio" 2>&1)"; then
			printf '%s cannot be read by afinfo: %s' "$name" "$(tr '\n' ' ' <<<"$info")"
			return
		fi
		container="$(sed -n 's/^File type ID: *//p' <<<"$info" | head -n 1)"
		duration="$(sed -n 's/^estimated duration: *\([0-9.]*\) sec.*/\1/p' <<<"$info" | head -n 1)"
		if [ "$container" != m4af ]; then
			printf '%s is not an m4af file (afinfo: %s)' "$name" "${container:-none}"
		elif ! grep -Eq '^Data format: .*, aac ' <<<"$info"; then
			printf '%s is not aac audio (afinfo: %s)' "$name" "$(grep -m 1 '^Data format:' <<<"$info" || echo none)"
		elif ! positive "$duration"; then
			printf '%s has no positive duration (afinfo: %s)' "$name" "${duration:-none}"
		fi
	elif [ "$want_container" = mp4 ]; then
		printf '%s cannot be probed: neither ffprobe nor afinfo is on the PATH' "$name"
	fi
}

# positive N succeeds when N is a decimal number greater than zero.
positive() {
	[[ "$1" =~ ^[0-9]*\.?[0-9]+$ ]] && [[ "$1" =~ [1-9] ]]
}

# doctor_numbers counts doctor's check lines by status. Its lines name local paths, so the
# report, which is attached to the release, carries counts rather than the lines.
doctor_numbers() {
	awk '$1 == "ok" || $1 == "warn" || $1 == "fail" { c[$1]++ }
		END { printf "ok=%d, warn=%d, fail=%d", c["ok"], c["warn"], c["fail"] }' "$1"
}

# step NAME MANIFEST CMD... runs CMD, logs its output and appends a report row.
step() {
	local name="$1" manifest="$2"
	shift 2
	n=$((n + 1))
	local log="${work}/step-${n}.log" start end code=0
	echo "== ${n}. ${name}: $*"
	start="$(date +%s)"
	"$@" >"$log" 2>&1 || code=$?
	end="$(date +%s)"
	local numbers="" evidence bad
	if [ "$manifest" = doctor ]; then
		numbers="$(doctor_numbers "$log")"
	elif [ -n "$manifest" ]; then
		# A say step passes on evidence, not on its exit status alone: audio a decoder reads
		# as what its extension names, and a complete manifest whose every chunk record the
		# verifier passed, agreeing with its summary, with real STT calls behind it.
		evidence="$(say_evidence "$manifest" "${manifest%.json}")"
		numbers="$(head -n 1 <<<"$evidence")"
		bad="$(sed -n 2p <<<"$evidence")"
		if [ "$bad" != ok ] && [ "$code" -eq 0 ]; then
			code=1
			echo "release-check.sh: ${bad:-no verdict on the evidence}" >>"$log"
			numbers="${numbers}; ${bad:-no verdict on the evidence}"
		fi
	else
		numbers="$(grep -Eo '^(ok|FAIL|---) .*' "$log" | tail -n 3 | tr '\n' ' ' | tr '|' '/')"
	fi
	if [ "$code" -ne 0 ]; then
		failed=$((failed + 1))
		echo "   FAILED (exit ${code}); log: ${log}"
		tail -n 20 "$log" | sed 's/^/   | /'
	fi
	# shellcheck disable=SC2016 # the backticks are Markdown code spans, not substitutions
	printf '| %d | %s | `%s` | %d | %d | %s |\n' "$n" "$name" "$(redact "$*")" "$code" "$((end - start))" "$(redact "$numbers")" >>"$report"
}

# Every sample opts in to STT verification: off is the default (D23), but the release check
# exists to exercise the verifier, and the verified and stt_calls counts of its manifests are
# what the report records. doctor --deep only warns on an STT problem under the default off
# mode, so these samples are the steps that fail when STT is down.
say() {
	local out="$1"
	shift
	"$bin" say --verify normal --no-cache --force -o "$out" "$@"
}

step "release binary provenance" "" provenance
step "doctor --deep" doctor "$bin" doctor --deep
step "English sample" "${work}/en.wav.json" say "${work}/en.wav" -f "${work}/en.txt"
step "Chinese sample" "${work}/zh.wav.json" say "${work}/zh.wav" -f "${work}/zh.txt"
step "Markdown sample" "${work}/md.wav.json" say "${work}/md.wav" -f "${work}/sample.md"
for effort in minimal low medium; do
	step "effort ${effort}" "${work}/effort-${effort}.wav.json" say "${work}/effort-${effort}.wav" --effort "$effort" -f "${work}/short.txt"
done
step "compressed format m4a" "${work}/en.m4a.json" say "${work}/en.m4a" -f "${work}/en.txt"
step "transcription round-trip (binary renders, independent STT)" "" \
	env RECITE_BIN="$abs_bin" go test -tags live -count=1 -run '^TestLiveSayHello$' -v ./internal/cli
step "transcription of a known sentence" "" \
	go test -tags live -count=1 -run '^TestLiveHTTPKnownSentence$' -v ./internal/stt

{
	echo
	if [ "$failed" -eq 0 ]; then
		echo "Result: PASS (${n} steps)"
	else
		echo "Result: FAIL (${failed} of ${n} steps failed)"
	fi
	echo
	echo "Logs: \`$(redact "$work")/\`"
} >>"$report"

echo "report: ${report}"
if [ "$failed" -ne 0 ]; then
	echo "release-check.sh: ${failed} of ${n} steps failed" >&2
	exit 1
fi
