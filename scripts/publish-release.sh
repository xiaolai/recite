#!/usr/bin/env bash
# Publishes a release that was built, signed and checked locally (REL-01..REL-05):
#
#   git tag -a vX.Y.Z -m "recite X.Y.Z" && git push origin vX.Y.Z
#   make release                                  # binaries carry the tag as their version
#   scripts/sign-notarize.sh                      # Developer ID, hardened runtime, Accepted
#   RECITE_LIVE=1 scripts/release-check.sh        # must end "Result: PASS"
#   scripts/publish-release.sh vX.Y.Z
#
# Before any network write it refuses unless: the tag exists locally and on origin and both
# point at HEAD; the tree is clean; dist/ holds a release-check report for exactly this
# version whose newest run passed (REL-05); and both darwin binaries carry a Developer ID
# signature with the hardened runtime and a notarisation that Apple Accepted (REL-02).
# Then it packages the archives, creates the GitHub release, checks every asset downloads
# anonymously and matches, updates the Homebrew tap (never to an older or equal version,
# REL-03), and dispatches the clean-Mac Gatekeeper check (REL-02#3).
#
#   DIST  the build directory (default: dist in the repository root)
set -euo pipefail

repo="xiaolai/recite"
tap="xiaolai/homebrew-tap"
root="$(cd "$(dirname "$0")/.." && pwd)"
dist="${DIST:-${root}/dist}"

fail() {
	echo "publish-release.sh: $*" >&2
	exit 1
}

if [ "$#" -ne 1 ]; then
	echo "usage: publish-release.sh vX.Y.Z" >&2
	exit 2
fi
version="$1"
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "${version} is not a vX.Y.Z version"
bare="${version#v}"
cd "$root"

# --- Local checks; nothing below this block writes to the network until they all pass.

head="$(git rev-parse HEAD)"
local_tag="$(git rev-parse --verify --quiet "refs/tags/${version}^{commit}")" ||
	fail "tag ${version} does not exist locally"
[ "$local_tag" = "$head" ] || fail "tag ${version} is ${local_tag}, not HEAD ${head}"
# An annotated tag lists its tag object and, with ^{}, the commit it points at.
remote_refs="$(git ls-remote origin "refs/tags/${version}" "refs/tags/${version}^{}")"
remote_tag="$(awk -v t="refs/tags/${version}" '$2 == t "^{}" {peeled = $1} $2 == t {plain = $1}
	END {print (peeled != "" ? peeled : plain)}' <<<"$remote_refs")"
[ -n "$remote_tag" ] || fail "tag ${version} is not on origin; push it first"
[ "$remote_tag" = "$head" ] || fail "origin's tag ${version} is ${remote_tag}, not HEAD ${head}"
[ -z "$(git status --porcelain)" ] || fail "the working tree is not clean"

# REL-05: the newest release-check report for this version must say PASS.
report=""
shopt -s nullglob
for r in "${dist}"/release-check-*.md; do
	if grep -Fxq -- "- Version: recite ${version}" "$r"; then
		report="$r" # the names sort by their UTC timestamp, so the last match is the newest
	fi
done
shopt -u nullglob
[ -n "$report" ] || fail "no release-check report for ${version} in ${dist}; run RECITE_LIVE=1 scripts/release-check.sh"
grep -Eq '^Result: PASS( |$)' "$report" || fail "${report} did not pass; fix the failures and run the check again"

# REL-02: the darwin binaries are signed, hardened and notarised.
for arch in arm64 amd64; do
	bin="${dist}/recite-darwin-${arch}"
	[ -s "$bin" ] || fail "${bin} is missing; run make release and scripts/sign-notarize.sh"
	codesign --verify --strict --verbose=2 "$bin"
	sig="$(codesign -dvvv "$bin" 2>&1)"
	grep -Eq 'flags=0x[0-9a-f]+\(runtime\)' <<<"$sig" || fail "${bin} lacks the hardened runtime flag"
	grep -Eq '^Authority=Developer ID Application: ' <<<"$sig" || fail "${bin} is not signed with a Developer ID Application identity"
	info="${dist}/notary-info-darwin-${arch}.txt"
	[ -s "$info" ] || fail "${info} is missing; run scripts/sign-notarize.sh"
	grep -Eq '^ *status: Accepted$' "$info" || fail "${info} does not say Accepted"
done

# --- Package and publish.

DIST="$dist" "${root}/scripts/package-release.sh"
stage="$(mktemp -d)"
trap 'rm -rf -- "$stage"' EXIT
assets=(recite-darwin-arm64.tar.gz recite-darwin-amd64.tar.gz recite-linux-amd64.tar.gz recite-linux-arm64.tar.gz)
for a in "${assets[@]}"; do
	cp -p -- "${dist}/${a}" "${stage}/${a}"
done
# A release-only SHA256SUMS: dist/SHA256SUMS also lists the bare binaries and zips.
(cd "$stage" && shasum -a 256 -- "${assets[@]}" >SHA256SUMS)
cp -- "$report" "${stage}/release-check-${version}.md"

notes="${stage}/notes.md"
cat >"$notes" <<NOTES
Install with \`brew install xiaolai/tap/recite\`, or download the archive for your platform,
check it against \`SHA256SUMS\` and put \`recite\` on your \`PATH\`.

The macOS binaries are signed with a Developer ID and notarised; the first run needs a
network connection so Gatekeeper can fetch the notarisation ticket.
\`release-check-${version}.md\` is the live release check this build passed.
NOTES

gh release create "$version" --repo "$repo" --verify-tag --title "recite ${version}" \
	--notes-file "$notes" \
	"${stage}/${assets[0]}" "${stage}/${assets[1]}" "${stage}/${assets[2]}" "${stage}/${assets[3]}" \
	"${stage}/SHA256SUMS" "${stage}/release-check-${version}.md"

# Every asset must download without credentials and match what was uploaded.
download="${stage}/download"
mkdir -p -- "$download"
for a in "${assets[@]}" SHA256SUMS "release-check-${version}.md"; do
	curl --fail --silent --show-error --location --retry 5 --retry-all-errors \
		-o "${download}/${a}" "https://github.com/${repo}/releases/download/${version}/${a}"
	cmp -- "${stage}/${a}" "${download}/${a}" || fail "downloaded ${a} differs from the uploaded file"
done
echo "all release assets download anonymously and match"

# --- Homebrew tap (REL-03).

DIST="$dist" "${root}/scripts/homebrew-formula.sh" "$version" \
	"https://github.com/${repo}/releases/download" "https://github.com/${repo}"
tapdir="${stage}/tap"
gh repo clone "$tap" "$tapdir" -- --depth 1 --branch main
formula="${tapdir}/Formula/recite.rb"
current=""
if [ -f "$formula" ]; then
	current="$(sed -n 's/^ *VERSION = "\([^"]*\)".*$/\1/p' "$formula")"
fi
"${root}/scripts/tap-version-guard.sh" "$current" "$bare" ||
	fail "the tap has ${current:-no version}; refusing to replace it with ${bare}"
mkdir -p -- "${tapdir}/Formula"
cp -- "${dist}/recite.rb" "$formula"
brew style "$formula"
msg="${stage}/commit-message.txt"
printf 'recite %s\n' "$bare" >"$msg"
git -C "$tapdir" add Formula/recite.rb
git -C "$tapdir" commit -F "$msg"
git -C "$tapdir" push origin HEAD:main
echo "tap updated to ${bare}"

# --- REL-02#3: a runner that never built recite downloads and runs it under Gatekeeper.
gh workflow run clean-mac.yml --repo "$repo" --ref main -f version="$version"
echo "dispatched clean-mac.yml for ${version}; follow it with: gh run list --repo ${repo} --workflow clean-mac.yml"
