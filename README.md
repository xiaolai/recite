# recite

recite is a Go CLI that turns text into speech using the local Codex ChatGPT sign-in.
Each take is checked by the model's own transcript and by audio guards (empty, length, status);
a take that fails is rendered again. Independent speech-to-text verification is opt-in
(`--verify normal` or `strict`) and is known to reject rare names and miss some word swaps.

> **Important notice: read before use.**
>
> recite relies on an **undocumented use of the ChatGPT sign-in** that the Codex CLI stores on
> your machine. OpenAI does not document or support this route. It may change or **stop working
> at any time**, without notice, and recite cannot work around that.
>
> **Your usage is your responsibility under OpenAI's terms.** Running recite spends your
> ChatGPT plan's usage. Read OpenAI's Terms of Use and usage policies, and decide yourself
> whether your use fits them. recite never asks for, stores or falls back to an API key.

## Quick start

```sh
go install github.com/xiaolai/recite/cmd/recite@latest   # or download a release binary
codex login                                               # sign in to Codex with ChatGPT once
recite doctor                                             # check the environment and the route
recite "Hello, world."                                    # writes speech-<timestamp>.wav
recite say -f chapter.md -o chapter.m4a                   # a Markdown file to a compressed file
recite check chapter.md                                   # what would be read, with no network
```

recite needs the [Codex CLI](https://github.com/openai/codex) signed in with a ChatGPT
account. Encoded formats other than WAV and PCM need `ffmpeg` on the `PATH` (on macOS, `m4a`
can also use the built-in `afconvert`).

## Install

- **Homebrew** (macOS and Linux): `brew install xiaolai/tap/recite`. `recite version` then
  reports the released version.
- **Release archives** for macOS (arm64, x86_64) and Linux (x86_64, arm64) on the
  [releases page](https://github.com/xiaolai/recite/releases) each hold the single static
  `recite` binary. Check the archive against `SHA256SUMS`, unpack it and put `recite` on your
  `PATH`.
- **From source**: `go install github.com/xiaolai/recite/cmd/recite@latest`, or `make build`
  in a checkout, which writes `bin/recite`.

### macOS Gatekeeper and offline first run

The macOS binaries are signed with a Developer ID and notarised by Apple. A bare command-line
binary cannot carry its notarisation ticket ("stapling" works only for apps, disk images and
installer packages), so on the **first run** macOS asks Apple's servers for the ticket. That
first run therefore needs a network connection; offline, Gatekeeper may refuse to start the
binary. After one online run, it starts offline too.

## Commands

`recite [global flags] <command> [flags] [arguments]`. A line with no known command is
`say`: `recite Hello` is `recite say Hello`. `recite help <command>` lists a command's flags.

| Command | What it does |
|---|---|
| `say` | Text to an audio file (the default command) |
| `render` | Render a batch or project file into many outputs |
| `check` | Text guard, spoken form and chunk plan; no network |
| `prepare` | Write the editable spoken-form script |
| `voices` | List voices; `--preview DIR` renders a sample of each |
| `doctor` | Check the environment and the route |
| `auth` | Login state (`status`), or sign in through Codex (`login`) |
| `usage` | Local ledger summary and Codex plan status |
| `cache` | Manage the take cache (`ls`, `prune`, `clear`) |
| `config` | Effective merged config and its sources (`show`, `path`) |
| `version` (also `v`, `--version`) | Version, commit, build date and Go version |
| `help` | Help for recite or a command |

### Global flags

These go before the command.

| Flag | Meaning |
|---|---|
| `--auth-source SOURCE` | Where credentials come from: `auto`, `codex` or `file` (default `auto`) |
| `--codex-bin PATH` | The Codex binary (also `RECITE_CODEX_BIN`) |
| `--codex-home DIR` | The Codex home directory |
| `--config PATH` | Read the user config from PATH (it must exist) |
| `--profile NAME` | Use the config profile NAME (also `RECITE_PROFILE`) |
| `--debug-dir DIR` | Write debug artifacts to DIR |
| `--log-file PATH` | Write the log to PATH |
| `--json` | Machine-readable output |
| `--no-color` | Disable colour |
| `-q`, `--quiet` | Errors only |
| `-v`, `--verbose` | More detail; repeatable |

### say

`recite say [flags] [TEXT...]` reads TEXT, a file (`-f`) or standard input, and writes one
audio file plus a JSON manifest next to it (`<output>.json`).

Input and output:

| Flag | Meaning |
|---|---|
| `-f`, `--file PATH` | Read text from PATH |
| `-o`, `--output PATH` | Write audio to PATH, or `-` for stdout (default `<file stem>.<format>` or `speech-<timestamp>.<format>`) |
| `--format FMT` | `wav`, `pcm`, `mp3`, `m4a`, `m4b`, `opus`, `ogg` or `flac` (default from the extension, else `wav`) |
| `--sample-rate HZ` | 16000, 22050, 24000, 44100 or 48000 (default 24000) |
| `--force` | Overwrite the output |
| `--manifest PATH` | Write the manifest to PATH |
| `--no-manifest` | Write no manifest |
| `--subtitles FMT` | Write `srt` or `vtt` subtitles next to the output |
| `--chapters` | Chapter markers from the headings (default on for `m4a`/`m4b` with headings) |
| `--split-sections` | One file per heading, `NN-<slug>.<format>`, into the `-o` directory |
| `--keep-rejected DIR` | Save every rejected take and its report in DIR |
| `--dry-run` | Print the chunk plan and estimates (cache hits, audio, wall time, list price); no network, no login |
| `--play` | Play the output when done |

Voice and delivery:

| Flag | Meaning |
|---|---|
| `--voice VOICE` | `alloy`, `ash`, `ballad`, `coral`, `echo`, `sage`, `shimmer`, `verse`, `marin` or `cedar` (default `cedar`) |
| `--model MODEL` | `gpt-realtime-2.1`, `gpt-realtime-2` or `gpt-realtime-2.1-mini` (default `gpt-realtime-2.1`) |
| `--model-unchecked` | Accept any `--model` name |
| `--speed SPEED` | 0.25 to 1.5 in steps of 0.01 (default 1.0) |
| `--effort EFFORT` | Reasoning effort: `minimal`, `low`, `medium`, `high` or `xhigh` (default `minimal`) |
| `--retry-effort LIST` | Efforts for attempts 1, 2, 3…, the last repeating, e.g. `minimal,low,medium` |
| `--emotion PRESET` | `neutral`, `warm`, `calm`, `excited`, `serious`, `sad`, `whisper`, `newsreader` or `storyteller` |
| `--style TEXT` | Free-text delivery note, sent after the rules as "Delivery:" |
| `--loudness PRESET` | `podcast`, `audiobook` or `none` (default `podcast` for encoded formats, `none` for WAV, PCM and stdout) |

Text handling (shared with `check`, `prepare`, `render` and `config`):

| Flag | Meaning |
|---|---|
| `--input-format FORMAT` | `text`, `markdown`, `script` (`NAME:` dialogue with a `[voices]` map) or `spoken` (a `prepare` script). Default: `markdown` for `.md`/`.markdown`, `script` for `.tts`/`.script`, else `text` |
| `--max-input BYTES` | Input size limit (default 10 MiB) |
| `--guard POLICY` | Text guard: `fix`, `strict`, `warn` or `off` (default `fix`) |
| `--normalize MODE` | Spoken-form rewriting (numbers, dates, symbols): `auto` or `off` |
| `--normalize-disable RULE,…` | Turn off individual spoken-form rules; repeatable |
| `--date-style STYLE` | Spoken dates: `us`, `uk` or `iso` |
| `--lexicon FILE` | Pronunciation lexicon, a TOML table of `TERM = { say = "…" }` or `{ hint = "…" }` (`hint` is best-effort; prefer `say`, see below) |
| `--directives` | Read `[pause 800ms]`, `[section Title]` and a leading `(direction)` per line |
| `--marks` | Read inline speech marks such as `{{spell\|API}}` (default on for spoken input) |
| `--no-marks` | Read `{{…}}` as plain text |
| `--code MODE` | Markdown code blocks: `skip`, or `announce` ("Code block omitted.") |
| `--read-alt` | Read the alt text of Markdown images |
| `--chunk-seconds SECONDS` | Target length of a chunk (default 25) |

Two pronunciation and stress controls are best-effort, not verified:

- A lexicon `hint` entry leaves the text unchanged and only asks the model to pronounce the term
  a certain way. Speech-to-text cannot check a pronunciation, so `hint` is unverifiable by STT
  and its effect is not guaranteed. Prefer `say`, which rewrites the spoken text; with `--verify normal` or `strict`, its output is
  verified against that rewrite (verification is off by default).
- The `{{emph|words}}` speech mark asks the model to stress the words and leaves the text
  unchanged. Whether the stress is audible is not verified.

Rendering and verification:

| Flag | Meaning |
|---|---|
| `--jobs N` | Render N chunks at once, 1 to 6 (default 3) |
| `--verify-jobs N` | Verify N takes at once (default 2 × `--jobs`) |
| `--session-chunks N` | Renders per speech session before it is replaced (default 50) |
| `--verify MODE` | `off`, `normal` or `strict` (default `off`; see below) |
| `--max-attempts N` | Renders per chunk (default 3) |
| `--request-extra-attempts N` | Extra renders for a chunk that reads like a request to the model (default 1) |
| `--allow-unverified` | Exit 0 even when a chunk stays unverified |
| `--strict-output` | Write no output when a chunk stays unverified |
| `--no-trim` | Keep the leading and trailing silence of each take |
| `--gap-sentence DUR` | Gap after a sentence, `DUR` or `MIN..MAX` (default `200ms..350ms`) |
| `--gap-list DUR` | Gap after a list item (default `350ms..500ms`) |
| `--gap-paragraph DUR` | Gap after a paragraph (default `550ms..800ms`) |
| `--gap-section DUR` | Gap after a section (default `1.1s..1.5s`) |
| `--gap-seed SEED` | Seed of the random gaps; the same seed gives the same gaps |
| `--no-cache` | Neither read nor write the take cache |
| `--refresh` | Render every chunk anew; the takes still go to the cache |

Verification modes. By default (`--verify off`) each take is checked by the model's own
transcript and the audio guards, and the first take that passes them is kept and marked
unverified in the manifest; nothing is sent to speech-to-text. `--verify normal` and `strict`
also transcribe every take with a separate speech-to-text model and compare it with the text.
That check is opt-in because calibration showed its limits: it respells rare names
(Albery → Albury, 许晚晴 → 许婉晴) and so rejects good takes, most often in Chinese, and it
sometimes writes back the original sentence over a spoken word swap, so it misses some swaps.
The numbers are in `dev-docs/calibration-20261012*.md` and the decision is D23 in
`dev-docs/design.md`.

### render

`recite render [flags] PROJECT.toml` renders every `[[output]]` of a project file in order,
each one through `say`. Paths are relative to the project file and may not leave its
directory. `[defaults]` holds settings shared by all outputs; each output may override them.

```toml
[defaults]
voice = "marin"

[[output]]
input = "chapters/01.md"
path = "audio/01.m4a"

[[output]]
input = "chapters/02.md"
path = "audio/02.m4a"
voice = "cedar"
```

It takes the `say` flags except `-f`, `-o` and `--manifest`, which are per output, plus:

| Flag | Meaning |
|---|---|
| `--fail-fast` | Stop at the first output that fails (default: render every output) |

### check

`recite check [flags] FILE` runs the text guard, the spoken-form rules and the chunk planner,
and reports findings, ambiguous spans and the plan. It needs no network and no login. It takes
the text-handling flags and `--speed`, which the time estimates assume.

### prepare

`recite prepare [flags] FILE` writes the spoken-form script: the exact words recite will send,
for you to edit and then render with `--input-format spoken`. It takes the
text-handling flags, `-o`/`--output PATH` (required; `-` for stdout), `--force`, and:

| Flag | Meaning |
|---|---|
| `--with-findings` | Add a comment per guard finding next to its chunk |

### voices

`recite voices` lists the voices.

| Flag | Meaning |
|---|---|
| `--preview DIR` | Render the sample sentence in every voice into DIR |
| `--text TEXT` | The sample text of `--preview` |
| `--effort EFFORT` | Reasoning effort of `--preview` |

### doctor

`recite doctor` checks the Codex binary, the sign-in and its expiry, the plan status, the
speech route, the encoders (`ffmpeg`, `afconvert`) and that the cache is writable.

| Flag | Meaning |
|---|---|
| `--deep` | Also render "Doctor check." and transcribe it (uses the service) |

### auth

`recite auth status` shows the login state; `recite auth login` signs in through Codex.

| Flag | Meaning |
|---|---|
| `--device-auth` | `login`: sign in with a device code (no browser on this machine) |

### usage

`recite usage` summarises the local usage ledger and shows the Codex plan status.

| Flag | Meaning |
|---|---|
| `--since string` | Only usage in this last period, such as `7d` or `12h` (default: all) |

### cache

`recite cache ls|prune|clear` manages the take cache. It never prompts.

| Flag | Meaning |
|---|---|
| `--older-than string` | `prune`: remove takes not used in this period, such as `30d` |
| `--max-size string` | `prune`: then remove the least recently used takes until the cache fits, such as `2GB` |
| `--yes` | `clear`: confirm removing every take |

### config

`recite config show` prints every setting with its effective value and where it came from;
`recite config path` prints the config file and cache locations. It takes the setting flags
of `say`, so you can see how a command line would resolve, plus:

| Flag | Meaning |
|---|---|
| `--input FILE` | Show the settings for FILE, including its Markdown or script front matter |

### version and help

`recite version` prints the version, commit, build date and Go version (`recite --json version` for JSON). `recite v` and `recite --version` do the same; `-v` is the verbosity flag, not the version.
`recite help [command]` prints help.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Internal error (bug) |
| 2 | Usage or input error: bad flag, bad input encoding, missing encoder, unknown voice |
| 3 | Auth: not signed in, not a ChatGPT sign-in, refresh failed |
| 4 | Route denied: the token was refused for this model or endpoint |
| 5 | Verification failed for at least one chunk after all attempts |
| 6 | Rate-limited or quota exhausted after backoff |
| 7 | Network, server or protocol failure after retries |
| 130 | Interrupted |

When several failures occur in one run, the reported code follows a fixed severity order,
independent of arrival order: 130 > 3 > 4 > 1 > 7 > 6 > 5 > 2.

## Configuration

Settings resolve in layers, each overriding the one before:

1. built-in defaults;
2. the user file, `$XDG_CONFIG_HOME/recite/config.toml`, else `~/.config/recite/config.toml`
   (or `--config PATH`);
3. the selected profile, a `[profile.NAME]` table in the user file, chosen by `--profile` or
   `RECITE_PROFILE`;
4. the project file of `render`;
5. the front matter of a Markdown or script input;
6. environment variables, such as `RECITE_VOICE`, `RECITE_MODEL`, `RECITE_EFFORT`,
   `RECITE_SPEED` and `RECITE_JOBS`;
7. command-line flags.

```toml
# ~/.config/recite/config.toml
voice = "marin"
speed = "1.05"

[profile.narrator]
voice = "cedar"
emotion = "storyteller"
```

A malformed environment variable is always an error, even when a flag overrides it.
`recite config show` prints each effective value and its source.

## The cache

Every take is stored with its verification record in a content-addressed cache, keyed by
everything that affects the audio. Rendering the same text again reuses the stored takes, which
costs no rendering usage; editing one paragraph re-renders only the chunks that changed. When
the verification rules have changed since a take was stored, it is verified again rather than
trusted.

- Location: `RECITE_CACHE_DIR`, else `$XDG_CACHE_HOME/recite`, else `~/Library/Caches/recite`
  on macOS and `~/.cache/recite` on Linux.
- `recite cache ls` shows its size and age; `recite cache prune --older-than 30d` or
  `--max-size 2GB` trims it; `recite cache clear --yes` empties it.
- `--no-cache` bypasses it for one run; `--refresh` renders anew but still stores the takes.

## License

ISC. See [LICENSE](LICENSE).
