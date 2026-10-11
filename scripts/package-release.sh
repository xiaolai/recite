#!/usr/bin/env bash
# Packs each release binary in dist/ into recite-<os>-<arch>.tar.gz with the LICENSE and
# README (REL-03), then rewrites dist/SHA256SUMS. Run it after scripts/sign-notarize.sh:
# tar keeps the signed darwin binaries byte for byte, and the archive keeps the
# executable bit, which a bare-binary download loses.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
dist="${DIST:-${root}/dist}"
cd "$root"

for platform in darwin-arm64 darwin-amd64 linux-arm64 linux-amd64; do
	bin="${dist}/recite-${platform}"
	if [ ! -x "$bin" ]; then
		echo "package-release.sh: ${bin} is missing; run make release first" >&2
		exit 1
	fi
	stage="$(mktemp -d)"
	cp -p -- "$bin" "${stage}/recite"
	cp -- LICENSE README.md "${stage}/"
	# COPYFILE_DISABLE keeps macOS from adding ._ resource files; the fixed owner keeps the
	# local account name out of the archive.
	COPYFILE_DISABLE=1 tar --uid 0 --gid 0 --uname root --gname root \
		-czf "${dist}/recite-${platform}.tar.gz" -C "$stage" recite LICENSE README.md
	rm -rf -- "$stage"
	echo "packed ${dist}/recite-${platform}.tar.gz"
done

"${root}/scripts/checksums.sh" "$dist"
