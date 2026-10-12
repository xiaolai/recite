#!/usr/bin/env bash
# Publishes a release that was built, signed and checked locally (REL-01..REL-05):
#
#   git tag -a vX.Y.Z -m "recite X.Y.Z" && git push origin vX.Y.Z
#   make release                                  # binaries carry the tag as their version
#   scripts/sign-notarize.sh                      # Developer ID, hardened runtime, Accepted
#   RECITE_LIVE=1 scripts/release-check.sh        # must end "Result: PASS"
#   scripts/publish-release.sh vX.Y.Z
#
# Before any network write it refuses unless: the tag exists locally and on xiaolai/recite
# and both point at HEAD; the tree is clean; dist/ holds a release-check report for exactly
# this version whose newest run passed (REL-05), checked a build of HEAD, and records the
# sha256 of one of the release binaries; and both darwin binaries carry a Developer ID
# signature with the hardened runtime and a notarisation that Apple Accepted for exactly
# their current bytes (REL-02).
# Then it packages the archives, creates the GitHub release, checks every asset downloads
# anonymously and matches, updates the Homebrew tap (never to an older or equal version,
# REL-03), and dispatches the clean-Mac Gatekeeper check (REL-02#3). A re-run after a later
# step failed is safe: an existing release must already hold identical assets, and a tap
# that already holds this exact formula is left alone.
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
# The tag is resolved in the repository the release is published to, not in origin, which
# may be a fork. An annotated tag is a tag object; follow it to the commit it names.
read -r tag_type tag_sha < <(gh api "repos/${repo}/git/ref/tags/${version}" --jq '.object.type + " " + .object.sha') ||
	fail "tag ${version} is not on ${repo}; push it first"
while [ "$tag_type" = tag ]; do
	read -r tag_type tag_sha < <(gh api "repos/${repo}/git/tags/${tag_sha}" --jq '.object.type + " " + .object.sha') ||
		fail "cannot read tag object ${tag_sha} on ${repo}"
done
[ "$tag_type" = commit ] || fail "${repo}'s tag ${version} names a ${tag_type:-nothing}, not a commit"
[ "$tag_sha" = "$head" ] || fail "${repo}'s tag ${version} is ${tag_sha}, not HEAD ${head}"
[ -z "$(git status --porcelain)" ] || fail "the working tree is not clean"

sha_of() {
	shasum -a 256 -- "$1" | awk '{print $1}'
}

# REL-05: the newest release-check report for this version must say PASS, and must be about
# these bytes: its commit is HEAD and its binary is one of the binaries published.
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
grep -Fxq -- "- Commit: ${head}" "$report" || fail "${report} did not check a build of HEAD ${head}"
checked_sha="$(sed -n 's/^- Binary sha256: \([0-9a-f]\{64\}\)$/\1/p' "$report")"
[ -n "$checked_sha" ] || fail "${report} records no binary sha256; run the check again"
checked=""
for platform in darwin-arm64 darwin-amd64 linux-amd64 linux-arm64; do
	if [ -s "${dist}/recite-${platform}" ] && [ "$(sha_of "${dist}/recite-${platform}")" = "$checked_sha" ]; then
		checked="$platform"
	fi
done
[ -n "$checked" ] || fail "${report} checked a binary (sha256 ${checked_sha}) that is not one of the release binaries in ${dist}"

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
	notarised="${dist}/notary-sha256-darwin-${arch}.txt"
	[ -s "$notarised" ] || fail "${notarised} is missing; run scripts/sign-notarize.sh"
	[ "$(cat -- "$notarised")" = "$(sha_of "$bin")" ] ||
		fail "${bin} is not the binary that was notarised; run scripts/sign-notarize.sh"
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

# A re-run after a later step failed finds the release already there. It never replaces an
# asset: the download check below must find every one identical to this run's, or it stops.
if gh release view "$version" --repo "$repo" >/dev/null 2>&1; then
	echo "release ${version} already exists; checking its assets match this build"
else
	gh release create "$version" --repo "$repo" --verify-tag --title "recite ${version}" \
		--notes-file "$notes" \
		"${stage}/${assets[0]}" "${stage}/${assets[1]}" "${stage}/${assets[2]}" "${stage}/${assets[3]}" \
		"${stage}/SHA256SUMS" "${stage}/release-check-${version}.md"
fi

# Every asset must download without credentials and match what this run staged.
download="${stage}/download"
mkdir -p -- "$download"
for a in "${assets[@]}" SHA256SUMS "release-check-${version}.md"; do
	curl --fail --silent --show-error --location --retry 5 --retry-all-errors \
		-o "${download}/${a}" "https://github.com/${repo}/releases/download/${version}/${a}" ||
		fail "${a} does not download from release ${version}"
	cmp -- "${stage}/${a}" "${download}/${a}" ||
		fail "release ${version}'s ${a} differs from this build's; refusing to continue"
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
	# An existing formula whose version cannot be read could be newer; never guess it away.
	[ -n "$current" ] || fail "the tap's Formula/recite.rb has no readable VERSION; refusing to replace it"
fi
if [ "$current" = "$bare" ] && cmp -s -- "${dist}/recite.rb" "$formula"; then
	# A re-run after the tap push: the tap already holds exactly this formula.
	echo "tap already has ${bare}"
else
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
fi

# --- REL-02#3: a runner that never built recite downloads and runs it under Gatekeeper.
gh workflow run clean-mac.yml --repo "$repo" --ref main -f version="$version"
echo "dispatched clean-mac.yml for ${version}; follow it with: gh run list --repo ${repo} --workflow clean-mac.yml"
