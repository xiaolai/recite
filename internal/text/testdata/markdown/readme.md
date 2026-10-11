# recite

[![build](https://example.com/badge.svg)](https://example.com/ci) ![logo](logo.png "Logo")

**recite** turns text into *verified* speech. It reads a file, renders each chunk, and
checks every take with a transcription before it keeps it.

<p align="center">A centred note for GitHub.</p>

## Install

```sh
brew install recite
```

Or build it from source with `make build`.

## Usage

1. Write some text.
2. Run `recite say -f notes.md`.
3. Listen to `notes.wav`.

| Flag | Meaning |
|------|---------|
| `-o` | output |

See the [design notes](docs/design.md) for details &amp; caveats. Visit <https://example.com>.

---

Licensed under MIT.
