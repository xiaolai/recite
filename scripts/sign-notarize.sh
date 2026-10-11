#!/usr/bin/env bash
# Signs and notarises the darwin binaries in dist/ (REL-02). Run after `make release`,
# on a Mac whose keychain holds the Developer ID Application identity and a notarytool
# profile (created once with `xcrun notarytool store-credentials`).
#
#   RECITE_SIGN_IDENTITY  the signing identity, e.g. "Developer ID Application: NAME (TEAMID)"
#   NOTARY_PROFILE        the notarytool keychain profile name
#
# A bare Mach-O binary cannot be stapled, so the notarisation ticket stays on Apple's
# servers and Gatekeeper fetches it online on first run. The README documents this.
set -euo pipefail

identity="${RECITE_SIGN_IDENTITY:?set RECITE_SIGN_IDENTITY to the Developer ID Application identity}"
profile="${NOTARY_PROFILE:?set NOTARY_PROFILE to the notarytool keychain profile}"
dist="${DIST:-dist}"
here="$(cd "$(dirname "$0")" && pwd)"

for arch in arm64 amd64; do
	bin="${dist}/recite-darwin-${arch}"
	if [ ! -s "$bin" ]; then
		echo "sign-notarize.sh: ${bin} is missing; run make release first" >&2
		exit 1
	fi

	echo "== sign ${bin}"
	codesign --force --sign "$identity" --options runtime --timestamp "$bin"
	codesign --verify --strict --verbose=2 "$bin"
	# Notarisation requires the hardened runtime; fail here if it did not stick. The output is
	# captured first: piped into grep -q under pipefail, codesign dies of SIGPIPE when grep
	# exits at its match, and a present flag reads as missing.
	sig="$(codesign --display --verbose=2 "$bin" 2>&1)"
	if ! grep -Eq 'flags=0x[0-9a-f]+\(runtime\)' <<<"$sig"; then
		echo "sign-notarize.sh: ${bin} lacks the hardened runtime flag" >&2
		exit 1
	fi

	zip="${dist}/recite-darwin-${arch}.zip"
	rm -f -- "$zip"
	ditto -c -k --keepParent "$bin" "$zip"

	echo "== notarise ${zip}"
	submit_log="${dist}/notary-submit-darwin-${arch}.txt"
	# `submit --wait` exits 0 even for an Invalid submission, so the info status decides.
	xcrun notarytool submit "$zip" --wait --keychain-profile "$profile" | tee "$submit_log"
	id="$(awk '$1 == "id:" {print $2; exit}' "$submit_log")"
	if [ -z "$id" ]; then
		echo "sign-notarize.sh: no submission id in ${submit_log}" >&2
		exit 1
	fi
	info_log="${dist}/notary-info-darwin-${arch}.txt"
	xcrun notarytool info "$id" --keychain-profile "$profile" | tee "$info_log"
	if ! grep -Eq '^ *status: Accepted$' "$info_log"; then
		echo "sign-notarize.sh: submission ${id} is not Accepted; inspect it with: xcrun notarytool log ${id} --keychain-profile <profile>" >&2
		exit 1
	fi
done

# codesign rewrote the darwin binaries, so the checksums must be recomputed.
"${here}/checksums.sh" "$dist"
