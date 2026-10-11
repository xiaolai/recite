.PHONY: build test

build:
	go build -o bin/recite ./cmd/recite

test:
	go test -race ./...

# WP-0.2 adds: check, fuzz, bench, live, qualify, calibrate.
