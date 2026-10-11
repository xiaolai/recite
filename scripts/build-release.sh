#!/usr/bin/env bash
# Cross-compiles recite for the four release platforms into dist/ (REL-01).
# VERSION, COMMIT and DATE are injected the same way `make build` does, so
# `recite version` reports them. Called by `make release`.
set -euo pipefail

: "${VERSION:?set VERSION}"
: "${COMMIT:?set COMMIT}"
: "${DATE:?set DATE}"
dist="${DIST:-dist}"
buildinfo="github.com/xiaolai/recite/internal/buildinfo"
ldflags="-s -w -X ${buildinfo}.version=${VERSION} -X ${buildinfo}.commit=${COMMIT} -X ${buildinfo}.date=${DATE}"

rm -rf -- "$dist"
mkdir -p -- "$dist"
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
	os="${target%/*}"
	arch="${target#*/}"
	out="${dist}/recite-${os}-${arch}"
	echo "build ${out}"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
		go build -trimpath -ldflags "$ldflags" -o "$out" ./cmd/recite
done
"$(dirname "$0")/checksums.sh" "$dist"
