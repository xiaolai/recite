#!/usr/bin/env bash
# The live release checklist (REL-05). It uses the real service and real usage, so it
# runs only with RECITE_LIVE=1. Must exit 0 before a release tag is pushed; attach the
# report it writes to the release.
#
#   RECITE_LIVE=1 scripts/release-check.sh
#
#   RECITE_BIN  the binary to check (default: dist/recite-<GOOS>-<GOARCH> from make release)
#
# Steps: doctor --deep; English, Chinese and Markdown samples; three effort levels; one
# compressed format (m4a); a transcription round-trip (the live tests that render with the
# built binary and transcribe the result independently).
set -euo pipefail

if [ "${RECITE_LIVE:-}" != "1" ]; then
	echo "release-check.sh: set RECITE_LIVE=1; this spends real usage against the service" >&2
	exit 2
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
bin="${RECITE_BIN:-dist/recite-$(go env GOOS)-$(go env GOARCH)}"
if [ ! -x "$bin" ]; then
	echo "release-check.sh: ${bin} is not an executable; run make release or set RECITE_BIN" >&2
	exit 2
fi
# The round-trip test runs in its package directory, so it gets the binary by absolute path.
abs_bin="$(cd "$(dirname "$bin")" && pwd)/$(basename "$bin")"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
work="dist/release-check-${stamp}"
report="dist/release-check-${stamp}.md"
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

failed=0
{
	echo "# recite release check"
	echo
	echo "- Started: ${stamp}"
	echo "- Binary: \`${bin}\`"
	echo "- Version: $("$bin" version | head -n 1)"
	echo
	echo "| # | Step | Command | Exit | Seconds | Key numbers |"
	echo "|---|---|---|---|---|---|"
} >"$report"

n=0
# manifest_numbers prints the run status and counts from a say manifest.
manifest_numbers() {
	local m="$1"
	if [ ! -s "$m" ]; then
		printf 'no manifest'
		return
	fi
	grep -Eo '"(status|chunks|verified|unverified|renders|stt_calls|duration_s)": ?("[a-z-]+"|[0-9.]+)' "$m" |
		awk -F': ?' '!seen[$1]++ {gsub(/"/, ""); printf "%s%s=%s", sep, $1, $2; sep=", "}'
}

# doctor_numbers counts doctor's check lines by status. Its lines name local paths, so the
# report, which is attached to the release, carries counts rather than the lines.
doctor_numbers() {
	awk '$1 == "ok" || $1 == "warn" || $1 == "fail" { c[$1]++ }
		END { printf "ok=%d, warn=%d, fail=%d", c["ok"], c["warn"], c["fail"] }' "$1"
}

# redact replaces the repository root with . and the home directory with ~, so no row names
# the local account or the checkout's location.
redact() {
	local s="$1"
	s="${s//"$PWD"\//./}"
	printf '%s' "${s//"$HOME"/\~}"
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
	local numbers=""
	if [ "$manifest" = doctor ]; then
		numbers="$(doctor_numbers "$log")"
	elif [ -n "$manifest" ]; then
		numbers="$(manifest_numbers "$manifest")"
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

say() {
	local out="$1"
	shift
	"$bin" say --no-cache --force -o "$out" "$@"
}

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
	echo "Logs: \`${work}/\`"
} >>"$report"

echo "report: ${report}"
if [ "$failed" -ne 0 ]; then
	echo "release-check.sh: ${failed} of ${n} steps failed" >&2
	exit 1
fi
