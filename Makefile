# Tool versions are pinned here and run with `go run`, so nothing is installed globally.
GOLANGCI_LINT := github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
GOVULNCHECK   := golang.org/x/vuln/cmd/govulncheck@v1.9.0
FUZZTIME      := 60s

.PHONY: build test check fuzz bench live qualify calibrate

BUILDINFO := github.com/xiaolai/recite/internal/buildinfo
VERSION   ?= $(shell git describe --always --dirty 2>/dev/null)
COMMIT    ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   := -X $(BUILDINFO).version=$(VERSION) -X $(BUILDINFO).commit=$(COMMIT) -X $(BUILDINFO).date=$(DATE)

# Empty values (no git repository) fall back to dev/unknown at run time (FND-02).
build:
	go build -ldflags '$(LDFLAGS)' -o bin/recite ./cmd/recite

test:
	go test -race ./...

# The merge gate (FND-03). Each recipe line fails the target on a non-zero exit.
check:
	@out="$$(find . -name '*.go' -not -path './.*' -print0 | xargs -0 gofmt -l)"; if [ -n "$$out" ]; then echo "gofmt -l: unformatted files:"; echo "$$out"; exit 1; fi
	go vet ./...
	go run $(GOLANGCI_LINT) run ./...
	go test -race ./...
	go run $(GOVULNCHECK) ./...
	go run ./tools/acceptcheck

# Extended fuzzing: FUZZTIME per Fuzz* target, one target at a time.
fuzz:
	@set -e; for pkg in $$(go list ./...); do \
		list="$$(go test -list '^Fuzz' $$pkg)"; \
		for f in $$(printf '%s\n' "$$list" | grep '^Fuzz' || true); do \
			echo "fuzz $$pkg $$f"; \
			go test -run '^$$' -fuzz "^$$f$$" -fuzztime $(FUZZTIME) $$pkg; \
		done; \
	done

bench:
	go test -run '^$$' -bench . -benchmem ./...

# Suites against the real service; never part of check.
live qualify calibrate:
	RECITE_LIVE=1 go test -tags $@ -run . -count=1 ./...
