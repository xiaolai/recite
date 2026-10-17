# recite feature ledger

2026-10-11. Every feature of the Go CLI, with its milestone, status, dependencies and acceptance
criteria. This is the input to the implementation plan. The design behind each entry is in
[design.md](design.md) (section references as `§n`). Evidence IDs (`E1`…`E21`) point to the
evidence table in design §3.

## How to read this ledger

| Field | Meaning |
|---|---|
| **ID** | `AREA-NN`, stable. Never renumbered; retired features stay with status `rejected`. |
| **M** | Milestone in which the feature ships (table below). |
| **Status** | `proven`: the mechanism was measured working on route A · `designed`: specified, no known blocker · `open`: needs an answer (a `Q` in design §15.2) before or while it is built · `rejected`: will not ship, with a reason. |
| **Depends** | Features that must exist first. The plan orders work by this graph. |
| **Accept** | Verifiable acceptance criteria. Each top-level bullet has an implicit ID `FEATURE#n` (1-based, in order: `NET-03#2` is NET-03's second bullet). A feature is done only when every bullet is referenced by at least one test carrying a `// accept: NET-03#2` comment, and that test passes: in `make check` for deterministic criteria, in `make live` or `make qualify` for live ones. `tools/acceptcheck` enforces this mechanically (FND-03). |
| **Kinds of criteria** | *Deterministic*: fakes, golden files, unit tests; blocking in `make check`. *Benchmark*: timing and memory targets, measured by `make bench` on a stated machine, never as hard assertions under `-race`. *Qualification*: behaviour of the live model, stated with a sample size and a pass rate, never as "always". |

## Milestones

| M | Name | Outcome a user can see | Exit gate |
|---|---|---|---|
| M0 | Foundations | `recite version`; CI green | Repo skeleton, CI gate, fakes and fixtures exist |
| M1 | First sentence | `recite say "Hello" -o hello.wav` produces audio from the subscription (not yet verified in-tool) | Auth, mint, one session, one chunk, WAV, exit codes, redaction |
| M2 | Verified long-form | Any length of plain text becomes one WAV (STT-verified with `--verify normal` or `strict`, D23), in parallel, with retries | Segmentation, session pool, HTTP verifier, adjudication, assembly |
| M3 | Production CLI | Compressed formats, manifest, cache and resume, config, `doctor`, `voices`, JSON mode | Re-run is free; failures are diagnosable |
| M4 | Rich long-form | Markdown in; chapters, subtitles, loudness, playback; `usage` and `cache` commands | Audiobook-grade output from a `.md` file |
| M5 | Voices and projects | Multi-speaker scripts; project files producing many outputs | Podcast or dialogue from a script |
| M6 | *(dropped)* | Speech-input commands removed: the project is TTS only | — |
| M7 | Release 1.0 | Signed binaries, Homebrew, documentation | Installable by others |

## Index

| ID | Feature | M | Status |
|---|---|---|---|
| **Foundations** | | | |
| FND-01 | Repository skeleton and module layout | M0 | designed |
| FND-02 | Build metadata and `version` | M0 | designed |
| FND-03 | CI quality gate | M0 | designed |
| FND-04 | Fake realtime server and recorded fixtures | M0 | designed |
| FND-05 | Fake `codex` app-server helper | M0 | designed |
| FND-06 | Fake synthesizer and transcriber | M0 | designed |
| **Credentials** | | | |
| AUTH-01 | Token from `codex app-server` `getAuthStatus` | M1 | proven |
| AUTH-02 | Read-only `auth.json` fallback | M1 | proven |
| AUTH-03 | Subscription-only guard | M1 | designed |
| AUTH-04 | Expiry check and refresh delegated to Codex | M1 | designed |
| AUTH-05 | Secret hygiene, redaction and private artifacts | M1 | designed |
| AUTH-06 | `auth status` command | M1 | designed |
| AUTH-07 | Login-state probe and user guide | M1 | proven |
| AUTH-08 | `auth login` command | M3 | designed |
| **Network** | | | |
| NET-01 | Locked-down HTTP client | M1 | designed |
| NET-02 | Client-secret minting | M1 | proven |
| NET-03 | WebSocket session wrapper | M1 | proven |
| NET-04 | Typed event codec and drift detection | M1 | designed |
| NET-05 | Error classification | M1 | designed |
| NET-06 | Retry, backoff and circuit breaker | M2 | designed |
| NET-07 | Session lifecycle and recycling | M2 | proven |
| **Speech synthesis** | | | |
| SPK-01 | Render one chunk via an out-of-band response | M1 | proven |
| SPK-02 | Model selection | M1 | proven |
| SPK-03 | Voice selection and validation | M1 | proven |
| SPK-04 | Speed control | M1 | proven |
| SPK-05 | Versioned verbatim instructions (INSTR v2) | M1 | proven |
| SPK-06 | Session pool keyed by model, voice and speed | M2 | proven |
| SPK-07 | Delivery: emotion presets and free-text style | M4 | proven |
| SPK-08 | Pronunciation lexicon (`say` and `hint`) | M4 | proven (`hint` best-effort, D22) |
| SPK-09 | Multi-voice rendering | M5 | designed |
| SPK-10 | Reasoning effort control | M1 | proven |
| SPK-11 | Effort escalation on retry | M3 | open (Q10) |
| SPK-12 | Emphasis marks | M4 | designed (best-effort, D22) |
| **Text** | | | |
| TXT-01 | Input sources and decoding | M1 | designed |
| TXT-02 | Text normalisation | M1 | designed |
| TXT-03 | Duration-budget segmentation | M2 | designed |
| TXT-04 | Script detection and language resolution | M2 | designed |
| TXT-05 | Markdown to speech | M4 | designed |
| TXT-06 | Dialogue script format | M5 | designed |
| TXT-07 | Inline directives (pause, section, style) | M5 | designed |
| TXT-08 | Front matter | M4 | designed |
| TXT-09 | Text guard | M2 | designed |
| TXT-10 | Spoken-form engine and span mapping | M2 | designed |
| TXT-11 | English spoken-form rules | M2 | designed |
| TXT-12 | Chinese spoken-form rules | M3 | designed |
| TXT-13 | Speech marks | M3 | designed |
| TXT-14 | Request-like text detection | M2 | proven |
| TXT-15 | Spoken-script input format | M3 | designed |
| **Verification** | | | |
| VER-01 | HTTP transcription verifier | M2 | proven |
| VER-02 | Realtime transcription fallback | M2 | proven |
| VER-03 | Canonicaliser | M2 | designed |
| VER-04 | WER/CER scorer and alignment report | M2 | designed |
| VER-05 | Pre-STT guards | M2 | designed |
| VER-06 | Edge-insertion and protected-token guards | M2 | designed |
| VER-07 | Threshold calibration harness and mini calibration | M2 | designed |
| VER-11 | Full calibration | M3 | designed |
| VER-08 | Adjudication and retry state machine | M2 | designed |
| VER-09 | Verify modes and unverified handling | M2 | designed |
| VER-10 | Keyword prompt builder | M2 | proven |
| **Audio** | | | |
| AUD-01 | PCM buffers and WAV I/O | M1 | proven |
| AUD-02 | Silence trimming | M2 | designed |
| AUD-03 | Ordered assembly, gaps and fades | M2 | designed |
| AUD-04 | Encoding through ffmpeg or afconvert | M3 | designed |
| AUD-05 | Resampling | M3 | designed |
| AUD-06 | Loudness normalisation | M4 | designed |
| AUD-07 | Audio to stdout | M3 | designed |
| AUD-08 | Playback | M4 | designed |
| **Outputs** | | | |
| OUT-01 | Output path rules and atomic publication | M1 | designed |
| OUT-02 | Manifest v1 | M3 | designed |
| OUT-03 | Subtitles (SRT, VTT) | M4 | designed |
| OUT-04 | Chapters | M4 | designed |
| OUT-05 | Split output by section | M4 | designed |
| OUT-06 | Keep rejected takes | M3 | designed |
| **Run engine** | | | |
| RUN-01 | Planner | M2 | designed |
| RUN-02 | Scheduler and worker pools | M2 | designed |
| RUN-03 | Cancellation and graceful drain | M2 | designed |
| RUN-04 | Content-addressed take cache | M3 | designed |
| RUN-05 | Resume after interruption | M3 | designed |
| RUN-06 | Dry run with estimate | M3 | designed |
| RUN-07 | Project and batch files | M5 | designed |
| **Command line** | | | |
| CLI-01 | Subcommand router and help | M0 | designed |
| CLI-02 | `say` | M1 | designed |
| CLI-03 | Exit-code contract | M1 | designed |
| CLI-04 | `voices` | M3 | proven |
| CLI-05 | `doctor` | M3 | designed |
| CLI-06 | JSON mode (NDJSON events) | M3 | designed |
| CLI-07 | TTY progress | M3 | designed |
| CLI-08 | `cache` | M4 | designed |
| CLI-09 | `usage` | M4 | proven |
| CLI-10 | `config show / path` | M3 | designed |
| CLI-13 | `render` | M5 | designed |
| CLI-14 | `check` | M2 | designed |
| CLI-15 | `prepare` | M3 | designed |
| **Configuration** | | | |
| CFG-01 | Config file | M3 | designed |
| CFG-02 | Profiles | M3 | designed |
| CFG-03 | Environment overrides | M3 | designed |
| CFG-04 | Precedence engine | M3 | designed |
| **Observability** | | | |
| OBS-01 | Structured logging | M1 | designed |
| OBS-02 | Debug event dumps | M2 | designed |
| OBS-03 | Usage ledger | M3 | designed |
| OBS-04 | Codex plan status | M3 | proven |
| **Release** | | | |
| REL-01 | Cross-platform builds | M7 | designed |
| REL-02 | macOS signing and notarisation | M7 | designed |
| REL-03 | Homebrew formula | M7 | designed |
| REL-04 | User documentation and disclosure | M7 | designed |
| REL-05 | Live release checklist | M7 | designed |
| **Rejected** | | | |
| X-01 … X-15, AUD-09, CLI-11, CLI-12, INT-01 … INT-04 | See the last section | — | rejected |

---

## Foundations (M0)

### FND-01 · Repository skeleton and module layout
- **M** M0 · **Status** designed · **Depends** —
- **What:** Go module `github.com/xiaolai/recite` (binary `recite`; the module path is free on the Go proxy and pkg.go.dev, checked 2026-10-11), with the
  package layout of design §4, `cmd/recite/main.go`, `testdata/`, `internal/*/fake`.
  The Go version is pinned in `go.mod` and toolchain.
- **Accept:**
  - `go build ./...` and `go test ./...` pass on an empty implementation.
  - `go list -deps ./cmd/recite` shows no third-party module beyond the four approved in D6.
  - A `deps_test.go` fails if a new third-party module appears in `go.mod` without being added
    to its allowlist.

### FND-02 · Build metadata and `version`
- **M** M0 · **Status** designed · **Depends** FND-01, CLI-01
- **What:** Version, commit and build date injected with `-ldflags`; `recite version` prints
  them plus the Go version; `--json` prints an object.
- **Accept:**
  - A test build with injected values prints them exactly; an un-injected build prints `dev` /
    `unknown` rather than empty strings.

### FND-03 · CI quality gate
- **M** M0 · **Status** designed · **Depends** FND-01
- **What:** One `make check`, used by CI and locally: `gofmt -l`, `go vet`, `golangci-lint` (with
  staticcheck enabled inside it, not run separately), `go test -race ./...` (which also replays
  every fuzz seed corpus), `govulncheck`, the dependency allowlist test, and `tools/acceptcheck`.
  Extended fuzzing (`make fuzz`, 60 s per target) and benchmarks (`make bench`) run at each
  milestone exit and in a scheduled CI job, not on every commit (R#34, R#15).
- **Accept:**
  - Any single failing step makes the command exit non-zero. CI runs it on macOS and Linux. Its
    result is the only gate for merging.
  - `tools/acceptcheck` fails when a feature marked done in the ledger has an acceptance bullet
    with no `// accept:` reference, or when a reference names a bullet that does not exist.
  - `make check` finishes in under 5 minutes on the CI runners (benchmark, reviewed at milestone
    exits).

### FND-04 · Fake realtime server and recorded fixtures
- **M** M0 · **Status** designed · **Depends** FND-01
- **What:** An `httptest` server that serves both `POST /v1/realtime/client_secrets` and the
  WebSocket, replaying event sequences from `testdata/realtime/*.jsonl`. The sequences come from
  the probe event logs, with audio payloads replaced by generated PCM. Fault-injection hooks:
  close mid-response, `error` event, no `response.done`, slow deltas, unknown event types,
  HTTP 400/401/403/429/500 on mint.
- **Accept:**
  - Each fault is reachable from a test option.
  - The fixtures include at least the speak, OOB multi-chunk, answer-instead-of-read (E22),
    `error`-event and realtime-transcription sequences.
  - A test asserts the fixtures contain no `ek_` or `eyJ` strings, no request or session IDs, and
    no account identifiers.

### FND-05 · Fake `codex` app-server helper
- **M** M0 · **Status** designed · **Depends** FND-01
- **What:** A test binary (built in `TestMain`) that speaks the JSON-RPC subset over stdio:
  `initialize`, `initialized`, `getAuthStatus`, `account/read`, `account/rateLimits/read`. Modes
  are selected by environment: `chatgpt`, `apikey`, `logged-out`, `slow`, `crash`, `refresh-ok`,
  `refresh-fails`.
- **Accept:**
  - Every AUTH test runs against it, with no real `codex` needed in CI.

### FND-06 · Fake synthesizer and transcriber
- **M** M0 · **Status** designed · **Depends** FND-01
- **What:** Implementations of `Synthesizer` and `Transcriber` (design §4) that are scripted per
  chunk and attempt: return a take with chosen `said` text, duration and transcript, or a chosen
  error class.
- **Accept:**
  - Pipeline tests can script outcomes such as "chunk 3 fails twice then passes" and "chunk 5's
    said text has a preamble", and they run deterministically under `-race`.

---

## Credentials (AUTH)

### AUTH-01 · Token from `codex app-server` `getAuthStatus`
- **M** M1 · **Status** proven (E19) · **Depends** FND-05, NET-01
- **What:** Spawn `codex app-server --listen stdio://` once per process. Send `initialize` (client
  name `recite`), then `initialized`, then `getAuthStatus {includeToken: false, refreshToken:
  false}`; only when `authMethod` is `chatgpt`, a second `getAuthStatus {includeToken: true,
  refreshToken: false}` (the metadata-first order of AUTH-03 and design §5.1a). The child is kept alive for the process lifetime, because refresh (AUTH-04) and plan
  status (OBS-04) use it, and it is reaped on exit (SIGTERM, then SIGKILL after 2 s). The token
  travels only over the stdio pipe: never in argv, the environment, or a file. Binary lookup
  order: `--codex-bin`, `RECITE_CODEX_BIN`, `PATH`. `--codex-home` is passed through as
  `CODEX_HOME`. The child inherits the environment minus `OPENAI_API_KEY`. The child's stderr is
  drained into a bounded buffer (64 KiB) and logged only through the scrubber (AUTH-05).
- **Accept:**
  - Against the fake: the token is returned; the child is still running after the call and gone
    after `Close` or process exit (no zombie, verified by PID).
  - Times out after 10 s with a `codex-error`-class error (exit 3) naming Codex.
  - The fake child records its argv and environment: neither contains the token, and the
    environment has no `OPENAI_API_KEY`.
  - Live: completes in under 2 s, and the token's `aud` is `https://api.openai.com/v1`.
- **Why:** Works for file, keyring and auto credential storage, and lets Codex own token rotation
  (design D2).

### AUTH-02 · Read-only `auth.json` fallback
- **M** M1 · **Status** proven · **Depends** AUTH-03
- **What:** Used when `--auth-source file` is set, or in `auto` when no `codex` binary is found.
  Reads `$CODEX_HOME/auth.json` (default `~/.codex`) with a 64 KiB size cap. Uses only
  `auth_mode` and `tokens.access_token`; never writes the file.
- **Accept:**
  - The file is opened `O_RDONLY`; a test with a read-only directory passes.
  - Oversized or malformed files → exit 3 with a clear message.
  - The `OPENAI_API_KEY` field is never read: a test fixture holding a canary value there finds
    the canary nowhere in memory dumps of the Token struct, logs or requests.

### AUTH-03 · Subscription-only guard
- **M** M1 · **Status** designed · **Depends** —
- **What:** Accept only `authMethod == "chatgpt"` (app-server) or `auth_mode == "chatgpt"` (file).
  The tool has no API-key code path, and does not read the `OPENAI_API_KEY` environment variable.
  With an API-key login, `getAuthStatus {includeToken: true}` would return the API key itself
  (E27), so recite first calls it with `includeToken: false` and requests the token only when
  `authMethod` is `chatgpt` (design §5.1a). Any token that still arrives is registered with the
  scrubber and treated as secret, whatever the mode.
- **Accept:**
  - The fake in `apikey` mode → exit 3, "Codex is signed in with an API key; recite works only
    with a ChatGPT sign-in".
  - With `OPENAI_API_KEY` set in the environment, a full fake run sends no request carrying it
    (asserted at the fake server).
  - The fake in `apikey` mode: recite never calls `getAuthStatus` with `includeToken: true`
    (asserted at the fake). A fake that returns a canary key anyway, even with `includeToken:
    false`: the key appears in no request, log, error, artifact or output.

### AUTH-04 · Expiry check and refresh delegated to Codex
- **M** M1 · **Status** designed (Q6 answered: safe, `dev-docs/spikes/s0.1-refresh.md`) · **Depends** AUTH-01, NET-05
- **What:** Decode the JWT payload's `exp` (no signature check; it is only used for UX). If
  fewer than 10 minutes remain, or a mint returns `auth-expired`, call `getAuthStatus
  {includeToken: true, refreshToken: true}` (single-flight; after a failure the next refresh waits
  a backoff of 60 s doubling to 15 min) and retry the failed request once.
  The file source cannot refresh, so it exits 3 telling the user to run any `codex` command.
- **Accept:**
  - Fake `refresh-ok`: one mint 401 → one refresh → success; exactly one refresh even when 8
    workers hit 401 together (single-flight).
  - Fake `refresh-fails` → exit 3, with the login guide naming `<codex> login` for the Codex in use.
  - Q6 settled: a live refresh against an isolated `CODEX_HOME` leaves Codex itself working.

### AUTH-05 · Secret hygiene, redaction and private artifacts
- **M** M1 · **Status** designed · **Depends** OBS-01
- **What:**
  - `Token` and `ClientSecret` types whose `String`, `GoString`, `Format` and `MarshalJSON`
    return `<redacted>`.
  - A redacting `slog` handler that scrubs the record **message**, every attribute value
    including nested groups and `error` values, drops attributes named `authorization`, `token`,
    `secret` and `value`, and scrubs the patterns `eyJ[\w-]+\.[\w-]+\.[\w-]+` and `ek_[\w-]+`.
    In addition, every secret the process has seen (the token and each client secret) is
    registered with the scrubber and removed as an exact byte string, so opaque formats are
    covered too.
  - Errors shown to the user pass through the same scrubber.
  - `internal/fsx` creates every private directory (`0700`) and file (`0600`): cache, state,
    debug dumps, rejected takes. An existing private directory that is group- or world-accessible
    is tightened if owned by the user, otherwise the run exits 2.
  - At start-up the process sets `RLIMIT_CORE` to 0 on every platform, and on Linux also calls
    `prctl(PR_SET_DUMPABLE, 0)`, because a piped `core_pattern` collector ignores the core size
    limit (N6). This covers recite's own process only; the Codex child is Codex's
    responsibility, and the documentation says so.
- **Accept:**
  - An end-to-end fake run at `-vv` with `--debug-dir`, `--keep-rejected`, a manifest and the
    usage ledger produces artifacts. A test searches all of them, plus captured stdout and
    stderr, for the exact canary token and secret bytes, and finds zero matches.
  - The fake child writes the canary token to its stderr, returns it in an RPC error, and emits
    malformed JSON containing it; none of it reaches any output.
  - A canary logged in a message, a nested group and a wrapped `error` is scrubbed.
  - `fmt.Sprintf("%v %+v %#v %s", tok)` contains no secret.
  - Private directories and files are created with the stated modes from M1 onward; a
    pre-existing `0777` debug directory is tightened to `0700`.
  - `getrlimit(RLIMIT_CORE)` is 0 after start-up; on Linux `prctl(PR_GET_DUMPABLE)` is 0.
  - `internal/fsx` walks private paths with `O_NOFOLLOW | O_DIRECTORY` and changes modes with
    `fchmod` on the opened descriptor. A symlink where a private directory is expected → exit 2,
    and the symlink's destination is neither written nor chmodded (R#30).
  - `internal/fsx` opens each level of a private path relative to the descriptor of the level
    above it, and every later write, read and removal goes through the directory's held
    descriptor (`fsx.Dir`), never its path: a directory or an ancestor replaced by a symlink
    after its check redirects nothing, and the link's destination is neither written nor
    chmodded.

### AUTH-07 · Login-state probe and user guide
- **M** M1 · **Status** proven (E19, E27) · **Depends** AUTH-01, AUTH-02, AUTH-03, AUTH-04
- **What:** Before any network call, every command that needs credentials runs the probe order
  of design §5.1a: resolve the invocation context once (absolute Codex path, `CODEX_HOME`,
  credential source), choose the source, call `getAuthStatus` with `includeToken: false`, request
  the token only for `chatgpt`, apply the refresh decision, then classify into exactly one state:
  `ready`, `no-codex`, `codex-too-old`, `not-signed-in`, `no-sign-in-file`, `api-key`,
  `unsupported-auth`, `expired`, `codex-error`, `app-server-auth-unavailable`,
  `provider-without-openai-auth` (sayso `spec/credentials.md`). Classification never uses the exit code of `codex login status`.
  Any state but `ready` stops with exit 3 and a guide on stderr: what is wrong, fix commands
  rendered from the resolved context, and the link `guide.AuthURL`. With `--json`, the guide is
  an `auth_error` event `{state, message, fix_commands, steps, link}`, defined in the M1 events schema
  (`docs/schema/events.schema.json` v1, which WP-3.8 extends). Route denial (exit 4) is not a
  login state and does not suggest logging in.
- **Accept:**
  - Each state is driven by the fake (or a missing executable, or an unknown-method error) and
    produces exit 3 and a golden guide; `ready` produces no guide.
  - Every one of the eight `AuthMode` values, plus an unknown future string: `chatgpt` → token
    requested; `apikey` → `api-key`; the other six and the unknown value → `unsupported-auth`; in
    none of the non-`chatgpt` cases is the token requested.
  - Token outcomes, each yielding exactly one state: `chatgpt` with `exp` in 5 minutes under
    `--auth-source file` → `expired`; with `exp` in 5 minutes and a successful Codex refresh →
    `ready`; refresh failed → `expired`; `chatgpt` with a null token → `codex-error`; a token that is not
    a JWT has unknown expiry → `ready`.
  - Source resolution: a valid ChatGPT `auth.json` in a temporary `CODEX_HOME` with no executable
    → `ready` under `auto` and `file`; `no-codex` only under `--auth-source codex`, or under
    `auto` with neither an executable nor a usable file.
  - Context binding: with executable A selected by `--codex-bin`, executable B first on `PATH`,
    and `--codex-home` set, every probe call uses A and that home, and the printed fix commands
    name A's absolute path and `CODEX_HOME=<home>`.
  - With the executable found on `PATH` and the default home, the printed fix commands still name
    its absolute path, never a bare `codex`.
  - The `not-signed-in` guide contains `login`, `login --device-auth` and the device-code
    prerequisite; the `api-key` guide contains `logout`, `login` and the warning; the
    `unsupported-auth` guide names the mode and contains no logout command.
  - The slow-child fake (10 s) gives class `codex-error`, exit 3, and the `codex-error` guide.
  - A JSON-RPC error reply to `getAuthStatus` (`{"code": -32000, "message": "credential store
    unavailable"}`) gives `codex-error`, exit 3; an unknown-method error, with `config/read` missing
    too, gives `app-server-auth-unavailable`.
  - `--json` emits one `auth_error` event that validates against the M1 events schema.
  - A route-denied run (exit 4) prints no login guidance.
  - The classification runs before any request reaches the fake API server.
  - Live: `guide.AuthURL` resolves to HTTP 200 and the page mentions `codex login`.
  - Live: with the real sign-in, the state is `ready` in under 2 s.
  - When a Codex without `getAuthStatus` names the file store and `auth.json` is missing
    (`no-sign-in-file` after the store fallback), the guide asks for `codex login`, never for
    the same probe again, under `--auth-source auto` as under `codex`.

### AUTH-08 · `auth login` command
- **M** M3 · **Status** designed · **Depends** AUTH-07
- **What:** `recite auth login [--device-auth]` runs `<codex> login` (or `<codex> login
  --device-auth`) using the invocation context resolved by AUTH-07: the same absolute executable
  and the same `CODEX_HOME`, never a fresh `PATH` lookup. Stdin, stdout and stderr are inherited,
  so the user signs in through Codex's own flow; recite never sees the flow or stores anything.
  Afterwards it **closes the running Codex app-server child and starts a new one** before
  classifying again, because a running child keeps reporting the old login (A#5, reproduced).
  - Refused with exit 2, without running Codex, when stdin is not a terminal (the flow is
    interactive) or when `--json` is set (Codex's output would corrupt the NDJSON stream, A#7).
  - If the current state is `api-key` or `unsupported-auth`, it refuses and prints the AUTH-07
    guide instead of changing Codex's sign-in on the user's behalf. In the states a login cannot
    repair (`provider-without-openai-auth`, `app-server-auth-unavailable`) it prints the guide too.
- **Accept:**
  - With fake executable A selected by `--codex-bin`, fake B first on `PATH`, and `--codex-home`
    set: A is executed with the exact argv (`login` or `login --device-auth`), the selected home,
    and inherited streams; B is never executed.
  - After the login, the old app-server child has exited and a new child produced the reported
    state (a fake whose state changes during login proves the new state is reported).
  - Non-TTY stdin → exit 2 without running Codex; `--json` → exit 2 without running Codex.
  - In the `api-key`, `unsupported-auth`, `provider-without-openai-auth` and
    `app-server-auth-unavailable` states it runs neither `logout` nor `login`.

### AUTH-06 · `auth status` command
- **M** M1 · **Status** designed · **Depends** AUTH-01, AUTH-02, AUTH-04, AUTH-07
- **What:** Prints the login state (AUTH-07), the source (codex app-server or file), Codex binary
  path and version, auth method, plan type, and token expiry as a relative time. When the state is
  not `ready`, it prints the AUTH-07 guide. Never prints the token, email or account ID. Supports
  `--json`. It is the command a user runs to answer "is recite ready?" without rendering anything.
- **Accept:**
  - The output contains none of the token, email or account ID (checked against the fake's
    values).
  - Exit 0 only when the AUTH-07 state is `ready`; every other state exits 3 with its guide.

---

## Network (NET)

### NET-01 · Locked-down HTTP client
- **M** M1 · **Status** designed (Q8 answered: works, `dev-docs/spikes/s0.2-proxy.md`) · **Depends** —
- **What:** One `http.Client` for both REST and WebSocket dials:
  - A RoundTripper wrapper enforces an exact origin policy for any request carrying a bearer:
    scheme `https`, host `api.openai.com`, port 443 or empty, no userinfo, no `Host` header
    override. Everything else fails before dialling.
  - `CheckRedirect` returns an error, so no 3xx is followed, same-host or not.
  - Proxy from the environment.
  - TLS 1.2 minimum.
  - Per-class timeouts (design §5.2).
  - Tests inject fakes **below** the wrapper (through a test origin option), so the policy itself
    is always exercised. The option is compiled into every build but cannot reach a real host: it
    accepts only reserved hosts (loopback, localhost, example.com, `.test`) and requires a replaced
    trust store or transport, so a bearer can never be sent to a real server through it.
- **Accept:**
  - A table test over mint, STT and WebSocket dials against: another host, `http://`, port 8443,
    userinfo, `Host` override, and a 3xx to the same host and to another host. Every case fails
    before dialling or at the redirect, and the second host receives no request.
  - Q8: an integration test through a local CONNECT proxy completes a mint and a WebSocket
    session.

### NET-02 · Client-secret minting
- **M** M1 · **Status** proven (E1, E2, E10) · **Depends** NET-01, AUTH-01
- **What:** A typed builder for the session body of speech and realtime transcription (the
  VER-02 fallback). Translation sessions are out of scope. Sets `expires_after` to 600 s by
  default. Returns `ClientSecret{Value, ExpiresAt}`. Does not log the response body.
- **Accept:**
  - Golden JSON tests for each session type, including `reasoning`, `speed` and `tools: []`.
  - HTTP 400 with `param: session.audio.output.voice` maps to `invalid-request` and names the bad
    parameter.
  - 401 `missing_scope` maps to `route-denied`.

### NET-03 · WebSocket session wrapper
- **M** M1 · **Status** proven · **Depends** NET-01, NET-02
- **What:** `coder/websocket` dial with `Authorization: Bearer ek_…`, a 16 MiB read limit, no
  client pings, one reader goroutine feeding a channel, and writes serialised by a mutex.
  `Close` runs the library's `Close(StatusNormalClosure)` in a goroutine and calls `CloseNow`
  if it has not returned within 2 s. The library's own `Close` can block for up to 10 s (5 s to
  write the frame, 5 s waiting for the peer).
- **Accept:**
  - Against the fake: the client sends no ping frames during a 10-second render while the server
    sends none either (the regression for the measured `1011 keepalive ping timeout`).
  - Context cancellation unblocks a pending read: correctness is blocking (returns
    `context.Canceled`, within 2 s under `-race`); the 100 ms latency target is a benchmark.
  - A fake peer that never acknowledges the close frame: `Close` returns within 2.5 s, and no
    goroutine leaks (`goleak`).

### NET-04 · Typed event codec and drift detection
- **M** M1 · **Status** designed · **Depends** NET-03
- **What:** A Go struct per consumed event (design §6.2, plus the realtime transcription events
  of the VER-02 fallback), decoded by a `type` switch. Unknown types are counted per session and logged at
  debug. A missing required field is a `protocol` error carrying the event type.
- **Accept:**
  - Every fixture decodes.
  - A fixture with an extra unknown event type still renders.
  - A `response.done` without `response.status` → `protocol` error.
  - Fuzz: the decoder never panics on arbitrary bytes.

### NET-05 · Error classification
- **M** M1 · **Status** designed · **Depends** NET-02, NET-03
- **What:** One `Classify(err) Class` covering every case in the design §5.3 table, including
  WebSocket close codes and `error` event codes. Each class maps to an exit code (CLI-03) and a
  retry policy (NET-06).
- **Accept:**
  - A table-driven test with one row per §5.3 signal. Every error returned by `say` has a class;
    `internal` (exit 1) appears only for programming errors.
  - The terminal and `doctor` show a classified failure in recite's words, from its class, HTTP
    status and provider code. The provider's free-text message is diagnostic only (sayso
    `spec/http-and-mint.md`) and reaches only the debug log.

### NET-06 · Retry, backoff and circuit breaker
- **M** M2 · **Status** designed · **Depends** NET-05, VER-02
- **What:**
  - Exponential backoff with full jitter (base 1 s, ×2, cap 16 s): up to 5 attempts for
    `rate-limited`, 3 for `server` and `network`.
  - `rate_limits.updated` with `remaining == 0` pauses new renders until `reset_seconds`.
  - `route-denied` is tracked **per route**: speech mint, HTTP STT, realtime STT. A denied
    verifier route switches verification to the next route (VER-02). The run-wide breaker opens,
    with exit 4, only when the speech route is denied, or when every verifier route is denied
    and verification is not `off`.
- **Accept:**
  - The fake denies HTTP STT with 401 `missing_scope` while realtime STT works: the run completes
    with exit 0, uses the fallback for all remaining chunks, and records the switch.
  - The fake returning 429 twice then 200 → success, with total waiting inside the expected
    bounds (seeded jitter).
  - The fake returning 403 `missing_scope` on chunk 2 of 10 → exactly one more in-flight
    request at most, then exit 4.

### NET-07 · Session lifecycle and recycling
- **M** M2 · **Status** proven (E9–E11) · **Depends** NET-03, SPK-01
- **What:** A speech session renders many chunks. It is closed and replaced when it reaches 50
  minutes of age, or `--session-chunks` (default 50) renders, or after any `server`, `network`
  or `protocol` error. A new session mints a new secret.
- **Accept:**
  - With a fake clock, a session is recycled after the age limit without failing an in-flight
    chunk.
  - After an injected mid-response drop, the chunk is retried on a fresh session.

---

## Speech synthesis (SPK)

### SPK-01 · Render one chunk via an out-of-band response
- **M** M1 · **Status** proven (E9) · **Depends** NET-03, NET-04, SPK-05
- **What:** `SpeechSession.Render` sends `response.create` with `conversation: "none"`, the chunk
  as one `input_text` message, `metadata.chunk` and `metadata.attempt`, and an optional
  per-response `instructions` and `reasoning` (design §6.2). It collects audio and transcript
  deltas until `response.done`, which must carry the same `metadata.chunk`.
- **Accept:**
  - Against the fake: PCM is the concatenation of the deltas; `Said` is the concatenated
    transcript; usage is parsed.
  - A `response.done` with mismatched metadata → `protocol` error.
  - Hard timeout = 3 × estimated duration + 30 s.
  - Live: "Hello from recite." renders, and STT hears it exactly.

### SPK-02 · Model selection
- **M** M1 · **Status** proven (D3) · **Depends** NET-02
- **What:** `--model` with allowed values `gpt-realtime-2.1` (default), `gpt-realtime-2` and
  `gpt-realtime-2.1-mini`. The model goes into both the mint body and the WebSocket URL.
  `--model-unchecked` allows any string, for when OpenAI ships a new model.
- **Accept:**
  - An unknown model without `--model-unchecked` → exit 2 before any network call. Golden tests
    show the model in both places. Live: all three render a sentence.

### SPK-03 · Voice selection and validation
- **M** M1 · **Status** proven (E1) · **Depends** NET-02
- **What:** `--voice`, default `cedar`, validated locally against the ten voices. Error messages
  list the valid voices.
- **Accept:**
  - `--voice cove` → exit 2, with the list, and no network call.

### SPK-04 · Speed control
- **M** M1 · **Status** proven (E2, E3) · **Depends** NET-02
- **What:** `--speed` from 0.25 to 1.5 in steps of 0.01; it goes into `audio.output.speed`. The
  duration estimator divides by the speed.
- **Accept:**
  - 0.24 and 1.51 → exit 2 locally. Golden mint body. Live: speed 1.5 audio is shorter than
    speed 1.0 for the same text.

### SPK-05 · Versioned verbatim instructions (INSTR v2)
- **M** M1 · **Status** proven (E22, E23) · **Depends** —
- **What:** `instructions.V2` (design §6.3) is sent as both the session and the per-response
  instructions. The chunk's spoken form goes in the user message between `<<<` and `>>>`. The
  version is part of every cache key. `instructions.V1` is kept only for regression comparison
  (`--instructions-version 1`, hidden).
- **Accept:**
  - A test pins the exact V2 text: any change without a version bump fails.
  - Golden request: rules in `instructions`, and only the delimited text in the message.
  - Qualification: the 29-item request-like corpus (`probe_v2.py`) × 3 takes on
    `gpt-realtime-2.1` (87 takes): at least 83 of 87 verbatim on the first attempt (measured
    87/87), and at least 86 of 87 within the attempt limit. The Wilson 95% interval is
    reported.

### SPK-06 · Session pool keyed by model, voice and speed
- **M** M2 · **Status** proven (E8, E9) · **Depends** SPK-01, NET-07
- **What:** The pool hands a worker an idle session for the chunk's key, or opens one, up to
  `--jobs` sessions in total. When the pool is full and no idle session has the needed key, the
  least-recently-used **idle** session of another key is closed and replaced. A worker never
  waits for a key that no open session can serve. Effort is not part of the key (it is per
  response).
- **Accept:**
  - With 2 voices and `--jobs 3`, no more than 3 sessions are open at once, and no chunk is
    rendered on a session with the wrong voice (asserted at the fake).
  - `--jobs 1` with alternating voices completes, closing and opening a session at each switch.
  - `--jobs 3` with four distinct keys in sequence completes without waiting on an idle session.
  - Live: 6 parallel renders succeed.

### SPK-07 · Delivery: emotion presets and free-text style
- **M** M4 · **Status** proven (E24) · **Depends** SPK-01, SPK-05, VER-08
- **What:** `--emotion` with the presets of design §7.9 (`neutral warm calm excited serious sad
  whisper newsreader storyteller`), `--style "…"` for free text, plus profile, front-matter and
  script `(direction)` sources. Both are appended after INSTR v2 as one `Delivery:` paragraph,
  never inside the markers. The preset ID and version and the style text are part of the cache key.
- **Accept:**
  - The golden per-response instructions show V2 unchanged, followed by the delivery paragraph.
  - An unknown preset → exit 2 with the list.
  - Qualification: each preset × 3 passages × 2 takes (54 takes). At least 50 verify within the
    attempt limit. Over its 6 takes, `whisper` and `sad` have a **median** duration at least 20%
    longer and a median RMS at least 1 dB lower than `neutral`. Results are reported, not cherry-
    picked.

### SPK-08 · Pronunciation lexicon (`say` and `hint`)
- **M** M4 · **Status** proven (`hint` best-effort, D22) · **Depends** SPK-01, CFG-01, TXT-10
- **What:** Lexicon entries (design §7.8) in two modes:
  - **`say`** rewrites the spoken form (deterministic; verified against the rewrite). Status:
    proven.
  - **`hint`** leaves the text unchanged and adds a `Pronounce "X" as "Y".` line to that chunk's
    instructions (unverifiable by STT). Status: best-effort (D22; Q11 not run).
  Matching is whole-word for spaced scripts, substring for CJK, and case-sensitive by default.
  The pairs used are part of the cache key.
- **Accept:**
  - Unit: matching rules, including CJK, overlapping entries (longest wins), and entries inside
    speech marks being ignored.
  - `say`: the spoken form contains the rewrite, and verification passes against it (fake).
  - `hint` (best-effort, D22; Q11 not run): the README and `say --help` describe `hint` as
    best-effort and unverifiable by STT, and recommend `say`.

### SPK-09 · Multi-voice rendering
- **M** M5 · **Status** designed · **Depends** SPK-06, TXT-06
- **What:** Chunks carry a voice from the script's speaker map; the pool keys on it; assembly
  stays in script order.
- **Accept:**
  - A 3-speaker fake script renders with each chunk's session voice matching its speaker, and
    the output order matches the script.

### SPK-10 · Reasoning effort control
- **M** M1 · **Status** proven (E21) · **Depends** NET-02
- **What:** `--effort` takes `minimal | low | medium | high | xhigh` (default `minimal`), and is
  also settable through config, profile and `RECITE_EFFORT`. It sets the session's
  `reasoning.effort`; a per-response `reasoning` is sent only when an attempt overrides it
  (SPK-11). The undocumented value `none` is not exposed. Effort is part of the cache key and
  the manifest.
- **Accept:**
  - Invalid values → exit 2 locally, listing the five levels.
  - Golden mint body for each level.
  - Live: each of the five levels renders and verifies a sentence.

### SPK-11 · Effort escalation on retry
- **M** M3 · **Status** open (Q10) · **Depends** SPK-10, VER-08
- **What:** `--retry-effort minimal,low,medium` assigns an effort per attempt. The **effective
  effort** of an attempt is `ladder[attempt]` when a ladder is set (the last entry repeats),
  otherwise `--effort`. Sessions are minted with `--effort`; a response carries a per-response
  `reasoning.effort` **whenever** its effective effort differs from the effort of the session it
  runs on (N3). The default stays off until Q10 shows a benefit.
- **Accept:**
  - `--effort minimal --retry-effort minimal,low,medium`: attempt 1 sends no override, attempt 2
    sends `low`, attempt 3 sends `medium`.
  - `--effort high --retry-effort minimal,low`: attempt 1 sends `minimal`.
  - No ladder and a single `--effort` for the run: no response carries a per-response
    `reasoning` (every session is minted at that effort).
  - Two project outputs with different `--effort` and the same pool key: each response carries
    the override its own output needs (asserted at the fake).
  - An invalid ladder → exit 2.
  - Q10 report committed to `dev-docs/`, with a decision on the default ladder.

### SPK-12 · Emphasis marks
- **M** M4 · **Status** designed (best-effort, D22) · **Depends** TXT-13, SPK-07
- **What:** `{{emph|words}}` adds `Stress the word(s) "words".` to that chunk's instructions.
  The text is unchanged.
- **Accept:**
  - Unit: the instruction line is produced, and the text inside the markers is unchanged.
  - `{{emph}}` is best-effort (D22; Q12 not run): the README describes the mark as best-effort,
    its stress not verified.

---

## Text (TXT)

### TXT-01 · Input sources and decoding
- **M** M1 · **Status** designed · **Depends** CLI-01
- **What:** Exactly one of: positional text, `-f/--file`, or stdin (`-`, or no source when stdin
  is not a TTY). The input must be UTF-8 (a BOM is stripped). Size limit 10 MiB unless
  `--max-input` is raised.
- **Accept:**
  - Two sources → exit 2.
  - Invalid UTF-8 → exit 2, reporting the byte offset.
  - Empty input after normalisation → exit 2 "nothing to speak".

### TXT-02 · Text normalisation
- **M** M1 · **Status** designed · **Depends** TXT-01
- **What:** NFC and `\r\n` → `\n` only. Nothing is removed here: control, zero-width and bidi
  characters are detected and removed by the text guard (`G-CONTROL`), so every removal becomes
  a finding. A NUL byte is rejected at decoding (TXT-01, `G-ENCODING`). Until the guard lands in
  M2, M1 rejects control characters other than `\n` and `\t` with exit 2.
- **Accept:**
  - A golden table of tricky inputs. Normalising twice equals normalising once (fuzz).
  - `hello\u0000world` → exit 2 with the byte offset (TXT-01).
  - In M2, `a\u200bb` and a bidi override produce `G-CONTROL` findings with their locations.

### TXT-03 · Duration-budget segmentation
- **M** M2 · **Status** designed · **Depends** TXT-02, TXT-04, TXT-10
- **What:** The algorithm of design §7.2, applied to the spoken form with display spans carried
  along: blocks first, a speech-rate table per script, a `--chunk-seconds` budget (default 25),
  sentence packing, clause and then whitespace fallback, and an English abbreviation list.
  `{{pause}}` marks force a split. Each chunk records its boundary type (sentence, paragraph,
  list item, section), which AUD-03 uses for gaps.
- **Accept:**
  - Fuzz invariant: the chunk texts plus their separators reconstruct the spoken form exactly,
    and the display spans reconstruct the display text exactly.
  - Golden tests cover: `Mrs. Okafor`, `3.5%`, `e.g.`, quotes after full stops, CJK `。！？`,
    mixed English and Chinese, and a 2,000-character sentence with no punctuation.
  - No chunk exceeds 1.3 × the budget unless it is a single unbreakable token run (warned).

### TXT-04 · Script detection and language resolution
- **M** M2 · **Status** designed · **Depends** TXT-02
- **What:** Two separate properties per chunk:
  - **Script**, by counting runes per Unicode script, with a confidence. It selects the speech
    rate and the scorer (CER for Han, Hiragana, Katakana, Hangul and Thai; WER otherwise).
  - **Language**, with a **source** and a **certain** flag, resolved in order:
    1. `{{lang}}` mark, `--lang`, or front matter → certain;
    2. any Kana → `ja`, certain; Hangul → `ko`, certain;
    3. Han with no Kana → `default_han_lang` (default `zh`), **not** certain, because kanji-only
       Japanese exists;
    4. Latin → `default_latin_lang` (default `en`), **not** certain;
    5. otherwise unknown.
  - Any language, certain or not, selects the spoken-form rules. Only a **certain** language
    sends the STT `languages[]` hint and arms the language guard (R#16).
- **Accept:**
  - A table: English, French (`Bonjour, comment allez-vous ?`), Spanish, Chinese, Japanese
    (`東京都に行きます`, and `東京都` alone), Korean, Russian, mixed. The expected script,
    language, source, certainty and hint are produced.
  - French text with no `--lang`: language `en` (not certain), no STT hint, and a take whose STT
    reports `fr` is **not** rejected by the language guard.
  - `東京都` alone: `zh`, not certain, no hint.

### TXT-05 · Markdown to speech
- **M** M4 · **Status** designed · **Depends** TXT-03, `goldmark`
- **What:** The design §7.1 rules: headings become sections; lists, emphasis and links keep their
  text; code, HTML and tables are skipped with a counted warning (or `--code announce`); image
  alt text only with `--read-alt`.
- **Accept:**
  - Golden tests over a Markdown corpus (README-like, a blog post, a chapter with nested lists,
    a code-heavy doc). The skipped-content warning reports correct counts.

### TXT-06 · Dialogue script format
- **M** M5 · **Status** designed · **Depends** TXT-03, TXT-08
- **What:** The format of design §7.4: front-matter speaker map, `NAME:` lines, and continuation
  lines.
- **Accept:**
  - An unknown speaker or an empty line body → exit 2 with the line number. A golden parse of a
    40-line sample.

### TXT-07 · Inline directives (pause, section, style)
- **M** M5 · **Status** designed · **Depends** TXT-06
- **What:** `[pause 800ms]`, `[pause 1.5s]`, `[section Title]` and a leading `(direction)` per
  line. They are honoured in script format, and in text or Markdown only with `--directives`.
- **Accept:**
  - Pauses appear as exact silence in the assembled PCM (to sample precision).
  - Directives are never sent to the model as text (asserted at the fake).

### TXT-08 · Front matter
- **M** M4 · **Status** designed · **Depends** CFG-04
- **What:** A leading `+++` TOML block in Markdown or script input may set `title`, `voice`,
  `voices`, `style`, `lang`, `speed`, `effort` and `lexicon`. It sits between project and
  environment in precedence.
- **Accept:**
  - `config show --input file.md` reports these values with source "front matter".

### TXT-09 · Text guard
- **M** M2 · **Status** designed · **Depends** TXT-02
- **What:** The detectors, findings and policies of design §7.6. `--guard fix|strict|warn|off`,
  with per-code actions through `--guard-action CODE=ACTION` in M2, and in config `[guard]` once
  CFG-01 lands (M3). Findings carry code, line:column, severity, action and
  a before/after span. All edits are span-mapped, so the display text and the original stay
  traceable.
- **Accept:**
  - A golden findings report for the messy corpus (the 21 `probe_textnorm.py` items plus HTML,
    LaTeX, homoglyph, bidi-override, long-hash and marker-injection cases).
  - `strict` with any warn-level finding → exit 2 before any network call (asserted at the fake).
  - `G-MARKER`: input containing `>>>` cannot end the script early (golden request shows it
    escaped).
  - Fuzz: the guard never panics, and is idempotent (`guard(guard(x)) == guard(x)`).
  - Under `fix`, the guard never adds words; only the spoken-form stage does.

### TXT-10 · Spoken-form engine and span mapping
- **M** M2 · **Status** designed · **Depends** TXT-09, TXT-04
- **What:**
  - The framework of design §7.7: per-language rule tables with rule IDs, applied left to right
    over unprotected spans, building a display ↔ spoken span map.
  - `--normalize auto|off` and `--normalize-disable RULE,…`.
  - Ambiguity yields an `N-AMBIGUOUS` finding and leaves the span as written.
- **Accept:**
  - Span-map invariant (fuzz): the map is a list of segments `(display range, spoken range,
    rule ID)`. Display ranges are in order, do not overlap, and together cover the whole display
    text. Spoken ranges likewise cover the whole spoken form. One segment may map a short
    display range to a longer spoken range (`42` → `forty-two`), to an empty one (a removed
    footnote), or the reverse. Checked on `42`, `$3.5M`, `[1]` and `{{say:twenty twenty-six|2026}}`.
  - A disabled rule has no effect; the report lists which rule produced each rewrite.
  - Normalisation is idempotent on its own output.

### TXT-11 · English spoken-form rules
- **M** M2 · **Status** designed · **Depends** TXT-10
- **What:** The English column of the design §7.7 table: cardinals, decimals, negatives,
  ordinals, currency with scale suffixes, percentages, ranges, fractions, ISO and numeric dates
  (`--date-style`), times, years (only next to year cues), phone numbers, versions, units, math
  and symbols, abbreviations (context rules for `St.` and `Dr.`), Roman numerals in numbering and
  regnal contexts, and the built-in tech-term lexicon. Q13 may drop rules the model already gets
  right.
- **Accept:**
  - A golden table of at least 200 input → spoken pairs, including every §7.5 probe item.
  - The ambiguity cases (`1/2` alone, a bare `St.`, `IV` alone) produce `N-AMBIGUOUS`, not a guess.
  - Qualification: 18 unchanged `probe_textnorm.py` items plus three fixed replacement English
    sentences and the 30 number- and symbol-dense English sentences in `qualify-en.txt`
    (51 chunks). The replacements cover contextual fractions, ordinal temperature ranges, and
    quantities with currency. Normalised, 1 take each: at least 49 of 51 verify on the first
    render attempt, and all 51 within the attempt limit. The original ambiguity and deliberately
    misspelled probe items remain covered by deterministic tests. *(Amended by owner decision
    2026-10-11, design D19: the two `N-AMBIGUOUS` items can never verify, by TXT-11#2, and STT
    auto-corrects the misspelled one.)*

### TXT-12 · Chinese spoken-form rules
- **M** M3 · **Status** designed · **Depends** TXT-10
- **What:** The Chinese column of design §7.7. Numbers are read as 汉字: digit by digit for
  years, positional otherwise, with 两 where conventional. Also: currency placement (`¥1,280.50` →
  一千二百八十点五元), percentages (百分之…), temperatures (零下…摄氏度), fractions (…分之…),
  ranges (到), dates and times, version numbers, and English terms left as written.
- **Accept:**
  - A golden table of at least 150 pairs, including the `zh-*` probe items.
  - The E25 currency-order error cannot recur: a test pins the `¥` output.
  - Qualification: the fixed corpus of 30 number-dense Chinese sentences, normalised: at least 24
    of 30 verify on the first render attempt, and all 30 within the configured attempt limit.
    Report the first-attempt rate, renders and STT calls per chunk, and every rejection reason.
    Keep all corpus items unchanged for this qualification. This criterion measures pipeline
    completion and retry burden; verifier accuracy is gated separately by VER-07 and VER-11.
    *(Amended by owner decision 2026-10-11, design D19; previously 28 of 30.)*

### TXT-13 · Speech marks
- **M** M3 · **Status** designed · **Depends** TXT-10
- **What:** The inline syntax of design §7.8: `{{say:…|…}}`, `{{spell|…}}`, `{{digits|…}}`,
  `{{num|…}}`, `{{date|…}}`, `{{fraction|…}}`, `{{verbatim|…}}`, `{{pause:…}}`, `{{hint:…|…}}`,
  `{{emph|…}}`, `{{lang:…|…}}`. Enabled with `--marks` (default on for `script` and `spoken`
  input). Marked spans are protected from the guard and from normalisation.
- **Accept:**
  - Golden parse for every mark kind, nesting rejected, and malformed marks → `G-MARK` (exit 2
    under `strict`).
  - Marks never reach the model as literal braces (asserted at the fake).
  - `{{pause}}` produces exact silence and a chunk split.

### TXT-14 · Request-like text detection
- **M** M2 · **Status** proven (E22: the failure mode is real) · **Depends** TXT-09
- **What:** `G-REQUEST` (design §7.6) flags chunks that read like a request to the model:
  questions addressed to "you", imperative openings, "ignore … instructions", and Chinese
  请/帮我/你 patterns. Flagged chunks get one extra render attempt by default
  (`--request-extra-attempts 1`) and are counted in the risk summary. The text is never changed.
- **Accept:**
  - Detector precision and recall on a labelled set of 100 sentences (50 request-like, 50 not):
    recall ≥ 90%. Precision is reported but not gated, because a false flag costs only a
    potential extra attempt.
  - The extra attempt is applied only to flagged chunks (fake).

### TXT-15 · Spoken-script input format
- **M** M3 · **Status** designed · **Depends** TXT-13
- **What:** `--input-format spoken` reads `prepare` output: one chunk per paragraph, comment lines
  (`#| display: …`) carrying the display text, speech marks allowed. The guard and normalisation
  are skipped; segmentation keeps the user's paragraph boundaries.
- **Accept:**
  - Round trip: `prepare` then `say --input-format spoken` without edits produces the same plan
    (same spoken forms and cache keys) as `say` on the original.
  - A hand edit to one paragraph re-renders only that chunk.

---

## Verification (VER)

### VER-01 · HTTP transcription verifier
- **M** M2 · **Status** proven (E15) · **Depends** NET-01, AUTH-01
- **What:** `POST /v1/audio/transcriptions` as multipart with: `model=gpt-transcribe`; the trimmed
  take as WAV padded with 300 ms of silence on each side; `languages[]` (the documented field for
  this model) when TXT-04 allows a hint; `prompt` from VER-10; `include[]=logprobs`. Parses
  `text`, `languages` (a list of `{"code": …}` objects, possibly empty), `logprobs` and
  `usage.seconds`. One retry on a dropped connection (E26).
- **Accept:**
  - Golden multipart body, with `languages[]`.
  - `{"text":"Hello","languages":[{"code":"en"}]}` and `{"text":"Hello","languages":[]}` both
    decode.
  - A 401 `missing_scope` → `route-denied` for this route only (then fallback, VER-02).
  - Live: a known sentence returns its exact text and `languages[0].code` is `en`.

### VER-02 · Realtime transcription fallback
- **M** M2 · **Status** proven (E14) · **Depends** NET-03
- **What:** When VER-01 is route-denied, a realtime transcription session
  (`gpt-live-transcribe`) is used, with `turn_detection:
  null`. Audio is streamed at 8× pace from a separate sender goroutine. One commit per take; the
  session is reused across takes.
- **Accept:**
  - Fake: three sequential commits on one session yield three transcripts matched by `item_id`.
  - Switching to the fallback is logged once per run and recorded in the run result (the
    manifest form is asserted in OUT-02, M3).

### VER-03 · Canonicaliser
- **M** M2 · **Status** designed · **Depends** —
- **What:** The steps of design §8.3: NFKC and width folding, lower case, abbreviation expansion,
  number canonicalisation (English digits ↔ words, ordinals, decimals, percentages, years;
  Chinese digits ↔ numerals including 两), hyphens and apostrophes, punctuation removal.
- **Accept:**
  - Each of these pairs canonicalises equal:
    `Mrs. Okafor` / `Missus Okafor`; `42` / `forty-two`; `1999` / `nineteen ninety-nine`;
    `3.5%` / `three point five percent`; `四十二` / `42`; `two thousand` / `2,000`.
  - Fuzz: idempotent.

### VER-04 · WER/CER scorer and alignment report
- **M** M2 · **Status** designed · **Depends** VER-03, TXT-04
- **What:** Levenshtein over canonical words (WER) or runes (CER), chosen by the chunk's script.
  `score = 1 − errors / reference length`, clamped to [0, 1]. Default thresholds are decided
  (design §8.4): accept ≥ 0.95, second transcription 0.85–0.95, reject < 0.85, strict 0.98. The alignment yields substitution,
  insertion and deletion spans in original-text coordinates.
- **Accept:**
  - Known pairs give exact scores.
  - The report renders "expected X / heard Y" spans correctly for a substitution in the middle
    of a sentence.
  - Performance: 2,000-word chunks score in under 50 ms.

### VER-05 · Pre-STT guards
- **M** M2 · **Status** designed · **Depends** VER-03, AUD-02, TXT-10
- **What:** Three guards run before STT (design §8.1):
  - **Said:** the canonical `said` must equal the canonical **spoken form** of the chunk.
  - **Empty:** audio shorter than 0.2 s fails.
  - **Duration:** trimmed length must be within 0.5×–2.2× of the estimate.
  - **Status:** `response.done.status` must be `completed`. A `failed`, `incomplete` or
    `cancelled` response is rejected even if some audio arrived.
  A failing guard rejects the take without an STT call, and the reason is recorded. After STT,
  a **language guard** applies: when the chunk's language is *certain* (TXT-04) and STT reports
  a different first language, the take is rejected.
- **Accept:**
  - The fake synthesizer scripts each failure once, and the take is re-rendered.
  - The STT call count in the fake is zero for takes rejected by a **pre-STT** guard (said,
    empty, duration, status). The language guard runs after STT, and is skipped when
    `--verify off` (N7).
  - `Take.Status` carries `response.done.status`; completed and incomplete fake responses with
    identical audio produce different verdicts (N2).
  - A recorded answer-instead-of-read take (from E22) is rejected by the said guard.
  - A take whose `said` expands a unit that the spoken form already expanded passes.
  - `response.done.status = incomplete` with 2 s of audio received → rejected.
  - A Chinese chunk whose STT reports `languages[0].code = en` → rejected by the language
    guard.

### VER-06 · Edge-insertion and protected-token guards
- **M** M2 · **Status** designed · **Depends** VER-04
- **What:**
  - **Edge insertion:** two or more inserted tokens (CER scripts: four or more runes) before the
    first matched token or after the last → reject, whatever the score.
  - **Protected tokens:** any substitution or deletion that touches a protected token rejects
    the take, whatever the score. Protected tokens are: any token containing a digit or a
    number word produced by the spoken form; capitalised words not at the start of a sentence
    (names); lexicon keys; `{{spell}}`, `{{digits}}` and `{{say}}` spans. This closes the gap
    where one wrong number in a 60-word chunk still scores 0.983.
- **Accept:**
  - "Sure, here it is. <text>" and "<text> Hope that helps!" are rejected even at a score of
    0.96. A single inserted "the" is not rejected by this guard.
  - A 60-word chunk whose STT differs only in "forty-two" → "forty-three" is rejected in normal
    and strict mode, although its score is 0.983.
  - A 60-word chunk whose STT differs only in "Okafor" → "Okafur" is rejected.
  - **Content-word guard:** a substitution, deletion **or insertion** of any word that is not on
    the built-in function-word list triggers a second, independent transcription. The
    function-word list (articles, prepositions, conjunctions, auxiliaries, pronouns; per
    language) **excludes semantic operators**: negations (`not`, `no`, `never`, `n't`,
    `without`, `none`, `nor`; 不, 没, 没有, 别, 非, 无, 未), modal polarity (`can`/`cannot`,
    `must`/`must not`) and quantifiers (`all`, `some`, `any`, `every`, `only`; 都, 只). A
    mismatch on a semantic operator is treated like a content-word mismatch. If the same mismatch
    appears again at the same aligned position, the take is rejected whatever the score. If the
    second transcription matches, the first is treated as a recogniser slip (R#26).
  - A difference in a function word ("a" → "the") is left to the score.
  - A 60-word chunk where STT twice hears "rejected" for "approved" is rejected (score 0.983);
    where only one of the two transcriptions differs, it passes.
  - A 60-word chunk where STT twice hears an inserted "not" in the middle is rejected (score
    0.983), as is a twice-heard deletion of 不 in a Chinese chunk.

### VER-07 · Threshold calibration harness and mini calibration
- **M** M2 · **Status** designed · **Depends** VER-01, VER-04, VER-06
- **What:** A live harness (`make calibrate`, resumable, sharded across days if plan limits
  appear). It calibrates the **post-render verifier**: STT, the scorer, and the post-STT guards.
  - *Corrupted* candidates: a passage is edited (word swapped, clause dropped, preamble added,
    number changed, name changed, meaning-changing content word), and the **edited** text is
    rendered.
  - *Good* candidates: the original passage is rendered.
  - In calibration runs the said guard is **bypassed**, so every sample reaches STT; otherwise a
    corrupted take would be caught by its own transcript and the verifier would never be tested
    (N4). Said-guard catch rates are reported separately.
  - **Labels come from listening, not from construction:** the owner labels every held-out
    sample (does the audio say the original passage exactly?). Construction labels are used only
    to stratify; any disagreement is reported (R#22). Under D22 an independent machine labeller
    may stand in for listening; the report must then say plainly that its labels are machine
    labels, weaker evidence than listening.
  - Samples are split 60/40: thresholds may be proposed on the first part only and are evaluated
    on the held-out part. Results are reported per corruption type, with Wilson 95% confidence
    intervals.
  - This feature delivers the harness plus the **mini calibration**: English only, 20 good + 20
    corrupted candidates, so 16 held-out clips to label (about 2 minutes of listening). The full
    run is VER-11.
- **Accept:**
  - The harness asserts that every corrupted sample actually reached STT (a stub STT that
    always reports a perfect match makes the run **fail**, not pass).
  - The report `dev-docs/calibration-YYYYMMDD.md` exists with the method, the human labels, the
    per-type results and the confidence intervals.
  - On the held-out set: 0 false accepts on samples labelled corrupted, and at most 5% false
    rejects on samples labelled good. If either fails, the feature is **not** done: the
    thresholds or guards are changed (with that evidence) and the held-out evaluation is repeated
    on fresh samples.
    *(Superseded by D23, 2026-10-12: STT verification is opt-in and off by default, and the
    held-out gate is dropped. The held-out numbers describe the opt-in mode and are reported, not
    gated. This bullet now requires that the committed report states its held-out verdict, PASS
    or FAIL as measured, with the counts behind it.)*

### VER-11 · Full calibration
- **M** M3 · **Status** designed · **Depends** VER-07, TXT-12
- **What:** The VER-07 harness and method on Latin and Han text, at least 60 good + 60 corrupted
  candidates per script (48 held-out clips per script to label by listening, about 6 minutes
  each), about 40 minutes of rendered audio, sharded across days if plan limits appear.
- **Accept:**
  - The same held-out criteria as VER-07, per script, in a committed report.
    *(Superseded by D23, 2026-10-12: the held-out criteria are reported, not gated. This bullet
    now requires one committed report per script (Latin and Han) that states its held-out
    verdict, PASS or FAIL as measured; the harness self-test of VER-07#1 still applies.)*

### VER-08 · Adjudication and retry state machine
- **M** M2 · **Status** designed · **Depends** VER-05, VER-06, VER-01
- **What:** The state machine of design §8 (diagram): a gray-zone take is transcribed a second
  time and the maximum score counts; rejected chunks are re-rendered up to `--max-attempts`
  (default 3); request-like chunks (TXT-14) get one extra attempt. Logprob confidence is
  recorded on mismatched spans.
  - An **eligible** take is one that passed **every pre-STT guard** (said, empty, duration,
    status). In normal and strict modes it receives an STT score; off mode keeps the first
    eligible take without STT and marks it unverified (D19). Answers, empty, wrong-length and
    incomplete takes are never eligible.
  - On exhaustion, the eligible take with the highest STT score is kept and marked `unverified`.
  - If a chunk has **no** eligible take, the run writes no output file (whatever
    `--allow-unverified` says), exits 5, and reports the chunk. Silence or partial audio is never
    substituted.
- **Accept:** each path is a fake script, and each records the right attempts, STT calls and
  final state in the run result (the manifest form is asserted in OUT-02, M3):
  - pass first time;
  - gray then pass on the second STT;
  - gray twice then re-render then pass;
  - three rejects → unverified with the best eligible take kept;
  - a guard reject followed by an STT pass;
  - every attempt empty, every attempt an answer, or every attempt failing the duration guard →
    no output file, exit 5, and zero STT calls.

### VER-09 · Verify modes and unverified handling
- **M** M2 · **Status** designed · **Depends** VER-08, CLI-03
- **What:** `--verify strict|normal|off`, `--allow-unverified`, `--strict-output` (design §8.5).
  `off` still runs the guards. The default mode is `off` (D23, 2026-10-12; previously `normal`):
  STT verification is opt-in with `normal` or `strict`.
- **Accept:**
  - The exit code and output-file existence match this table for each mode and outcome:

    | Outcome | Default | `--allow-unverified` | `--strict-output` |
    |---|---|---|---|
    | All chunks verified | 0, file written | 0, file written | 0, file written |
    | Some chunk unverified | 5, file written | 0, file written | 5, no file |
    | Some chunk with no eligible take | 5, no file | 5, no file | 5, no file |

    The table above is for normal and strict mode. Off mode (D19):

    | Off-mode outcome | Default | `--allow-unverified` | `--strict-output` |
    |---|---|---|---|
    | Every chunk has an eligible take | 0, file written | 0, file written | 5, no file |
    | Some chunk has no eligible take | 5, no file | 5, no file | 5, no file |

    Both flags together keep strict-output precedence. Off-mode output remains unverified in
    reporting, manifests and cache records.
  - The default verify mode is `off` (D23): with no flag, config, profile or environment
    setting, `say` resolves `--verify` to `off`, and `config show` reports `verify.mode off` from
    the default layer. `--verify normal` and `strict` still select STT verification.

### VER-10 · Keyword prompt builder
- **M** M2 · **Status** proven (E15: a `prompt` was accepted and fixed "Okafor") · **Depends** TXT-04
- **What:** At most 20 keywords per chunk: capitalised words not at the start of a sentence,
  tokens containing digits, lexicon keys, and words not in a bundled 10k-word frequency list
  (English). Comma-joined. Never the full text (X-10).
- **Accept:**
  - The prompt for any chunk is under 30% of the chunk's tokens. Asserted by a fuzz test, so the
    prompt can never become the sentence.
  - Golden prompts for the sample corpus.

---

## Audio (AUD)

### AUD-01 · PCM buffers and WAV I/O
- **M** M1 · **Status** proven · **Depends** —
- **What:** PCM s16le, 24 kHz mono type; a RIFF/WAVE writer and reader (PCM format 1 only);
  `.pcm` raw output.
- **Accept:**
  - Round-trip test.
  - Files open in `afinfo` and `ffprobe` with the correct duration (live-gated on tool presence).
  - Writing a WAV over 4 GiB fails with a clear error rather than corrupting the header.

### AUD-02 · Silence trimming
- **M** M2 · **Status** designed · **Depends** AUD-01
- **What:** RMS over 10 ms windows; −50 dBFS threshold; leading and trailing silence trimmed with
  a 40 ms margin. `--no-trim` disables it.
- **Accept:**
  - Synthetic signals with known silence give trimmed lengths to within one window. A quiet but
    non-silent passage (−40 dBFS) is never trimmed.

### AUD-03 · Ordered assembly, gaps and fades
- **M** M2 · **Status** designed · **Depends** AUD-02, TXT-03
- **What:**
  - Join chunk PCM in index order with a silence per join chosen by boundary type. Each gap type
    is a fixed duration or a `MIN..MAX` range (`--gap-sentence`, `--gap-list`, `--gap-paragraph`,
    `--gap-section`, config `[gaps]`). Ranges are drawn per join from a PRNG seeded by the gap
    seed, both neighbours' cache keys and the gap type (design §9.1). Each drawn gap is recorded
    in the manifest.
  - Apply 5 ms linear fades at every chunk edge.
  - Stream the assembly as soon as a prefix is complete.
- **Accept:**
  - Assembled length = Σ chunk lengths + Σ gaps, to the exact sample.
  - Every drawn gap lies within its range; a fixed value gives exactly that value.
  - Determinism: two runs on the same input and seed give identical gaps; editing one chunk
    changes only the gaps next to it; changing `--gap-seed` changes them.
  - Over 1,000 draws, gaps cover the range (minimum and maximum within 5% of the bounds).
  - Invalid ranges (`MIN > MAX`, negative, over 10 s) → exit 2.
  - Changing gap settings causes zero re-renders.
  - No sample discontinuity above a threshold at joins (synthetic test).
  - Out-of-order completion produces the same bytes as in-order completion.

### AUD-04 · Encoding through ffmpeg or afconvert
- **M** M3 · **Status** designed · **Depends** AUD-03
- **What:**
  - Formats come from the extension or `--format`: `mp3 m4a m4b opus ogg flac`.
    Raw ADTS AAC output is unsupported; use `m4a` for AAC audio.
  - Encoders: `ffmpeg` (preferred), or `afconvert` for `m4a` on macOS.
  - PCM is piped through stdin; no shell is involved (`exec.Command` with arguments).
  - Bitrates are as in design §9.
  - Encoder presence is checked before any network call.
- **Accept:**
  - With no encoder available, `-o x.mp3` → exit 2 naming `ffmpeg`, and no request reaches the
    fake.
  - Live-gated: each supported encoded format is decoded to PCM; its decoded
    sample count divided by its decoded sample rate equals the assembled input
    duration ±50 ms. `ffprobe` confirms the expected codec and container.
  - `-o x.aac` or `--format aac` → exit 2 before any network call, with a
    diagnostic that raw ADTS AAC output is unsupported and suggests `m4a`.

### AUD-05 · Resampling
- **M** M3 · **Status** designed · **Depends** AUD-04
- **What:** `--sample-rate 16000|22050|44100|48000` through ffmpeg. WAV output at another rate
  also goes through ffmpeg.
- **Accept:**
  - The output header shows the requested rate, and the duration is preserved within 10 ms.

### AUD-06 · Loudness normalisation
- **M** M4 · **Status** designed · **Depends** AUD-04
- **What:** `--loudness podcast|audiobook|none` uses ffmpeg `loudnorm` in two passes with the
  design §9 targets. The default is `podcast` for compressed formats and `none` for WAV and PCM.
  When ffmpeg is missing and afconvert encodes m4a, the default is `none` with a warning that
  loudness is not normalised; an explicit preset needs ffmpeg (exit 2 without it).
- **Accept:**
  - Live-gated: the measured integrated loudness of the output is within ±1 LU of the target,
    and the true peak is at or below the ceiling.

### AUD-07 · Audio to stdout
- **M** M3 · **Status** designed · **Depends** AUD-03, RUN-02
- **What:** `-o -` streams the encoded output to stdout as soon as an ordered prefix is ready.
  WAV uses a streaming header (size fields set to the maximum); the other formats go through an
  ffmpeg pipe. Logs go only to stderr. `--json` together with `-o -` → exit 2.
  - Streaming cannot take bytes back, so `-o -` is incompatible with `--strict-output` and with
    two-pass loudness. With `-o -`, loudness defaults to `none`; an explicit `--loudness` other
    than `none`, or `--strict-output`, → exit 2 naming the conflict. A chunk failing after
    streaming has started ends the stream early, and the exit code reports it.
- **Accept:**
  - `recite say -f long.txt -o - | ffprobe -` reads a valid stream with the fake backend. Stdout
    contains audio bytes only.
  - `-o - --strict-output` and `-o - --loudness podcast` → exit 2 before any network call.
  - The last chunk failing after earlier chunks streamed → the stream ends and the exit code is
    5.

### AUD-08 · Playback
- **M** M4 · **Status** designed · **Depends** AUD-03
- **What:** `--play` plays the finished output with `afplay` on macOS, or `ffplay -nodisp
  -autoexit` / `paplay` on Linux. Playback is not counted in the exit code: a player failure is
  a warning.
- **Accept:**
  - A missing player gives a warning, exit 0.

---

## Outputs (OUT)

### OUT-01 · Output path rules and atomic publication
- **M** M1 · **Status** designed · **Depends** —
- **What:**
  - The default name is derived from the input stem, or `speech-YYYYMMDD-HHMMSS.wav`.
  - Writes go to a temp file created `O_EXCL` in the target directory, and are fsynced.
  - Without `--force`, the file is published with `link(temp, target)`, which fails if the target
    exists, so a file created after the pre-flight check is never overwritten; the temp name is
    then unlinked. With `--force`, it is published with `rename`, which replaces a symlink at the
    target rather than following it.
  - An existing target without `--force` is also checked before any network call, to fail fast.
- **Accept:**
  - A killed run (SIGKILL mid-write) never leaves a partial file at the target path.
  - An existing target without `--force` → exit 2 before any network call.
  - A target created between the pre-flight check and publication is not overwritten (exit 2,
    the run's audio kept at the reported temp path).
  - With `--force`, a symlink at the target is replaced by a regular file, and the link's
    destination is unchanged.

### OUT-02 · Manifest v1
- **M** M3 · **Status** designed · **Depends** RUN-01, VER-08
- **What:** The schema of design §11.2, written atomically next to the output. A JSON Schema file
  is published in the repository. Contains no secrets and no account identifiers.
- **Accept:**
  - Every fake-run manifest validates against the schema.
  - It contains no email, account ID or token (grep test).
  - The `start_s`/`end_s` timeline equals the assembly's.

### OUT-03 · Subtitles (SRT, VTT)
- **M** M4 · **Status** designed · **Depends** AUD-03, TXT-03
- **What:**
  - One cue per sentence, timed from the assembly timeline. Cues come from per-chunk durations;
    sentences inside one chunk share its time span in proportion to their character counts.
    This is an approximation and is documented as one.
  - Cues are capped at 2 lines × 42 characters (CJK: 2 × 16), with splits as needed.
- **Accept:**
  - The SRT and VTT files parse (VTT validator in tests).
  - Cue times are monotonic, and stay within the audio duration.
  - Chunk-boundary cues are accurate to within 10 ms.

### OUT-04 · Chapters
- **M** M4 · **Status** designed · **Depends** TXT-05, AUD-04
- **What:** Sections (Markdown headings, `[section]`) become chapter markers via ffmetadata for
  `m4a`/`m4b`, and a chapter list in the manifest for every format.
- **Accept:**
  - Live-gated: `ffprobe -show_chapters` lists the headings with the correct start times (±10
    ms).

### OUT-05 · Split output by section
- **M** M4 · **Status** designed · **Depends** OUT-04
- **What:** `--split-sections` writes one file per section into the output directory, named
  `NN-<slug>.<ext>`, plus one manifest for the whole run.
- **Accept:**
  - The file count equals the section count. Slugs are filesystem-safe (fuzz on Unicode
    headings). Re-runs are idempotent.

### OUT-06 · Keep rejected takes
- **M** M3 · **Status** designed · **Depends** VER-08
- **What:** `--keep-rejected DIR` saves `<chunk>-a<attempt>.wav` plus `.json` (reason, scores,
  transcript, alignment) for every rejected take.
- **Accept:**
  - A fake run with two scripted rejections writes exactly two pairs, and contains no secrets.

---

## Run engine (RUN)

### RUN-01 · Planner
- **M** M2 · **Status** designed · **Depends** TXT-03, SPK-10
- **What:** Produces the ordered chunk list with IDs (`c0001`…): display text, spoken form,
  findings, session key, effort, delivery, hints, cache key (over the spoken form) and estimated
  duration. Plans are pure: the same input and settings give the same plan.
- **Accept:**
  - A determinism test: the plan's JSON is byte-identical across runs and platforms.
  - Editing only display-only content (a removed footnote marker, say) leaves the cache key
    unchanged.

### RUN-02 · Scheduler and worker pools
- **M** M2 · **Status** designed · **Depends** RUN-01, SPK-06, VER-08
- **What:** The design §10 scheduler:
  - **One coordinator goroutine** owns all scheduling state. Render workers (`--jobs`, default
    3, maximum 6) and verify workers (`--verify-jobs`, default 2× jobs) receive one job at a time
    and send one result back; workers never enqueue work themselves, so the render → verify →
    re-render cycle cannot deadlock.
  - An **admission window** bounds memory: only chunks with index in the half-open interval
    `[low, low + W)` may be dispatched, where `low` is the lowest unfinished chunk (default
    `W = 8 × jobs`). At most `W` chunks are in flight or held. Completed PCM waiting for assembly is
    therefore bounded by `W` chunks. There is no temp-file spill.
  - Re-renders take priority over new chunks; assembly streams in order.
- **Accept:**
  - With a fake that has random latencies, 500 chunks complete under `-race` in order with no
    goroutine leak.
  - Chunk 1 stalls until its timeout while every later chunk succeeds instantly: no more than
    `W` chunks are ever held, and chunk 1's failure is reported.
  - Every verify result is a reject until attempts run out, with `--jobs 1 --verify-jobs 1`: the
    run terminates (exit 5) within the test deadline.
  - `--jobs 7` → exit 2 before any network call. With `--jobs 6`, at most 6 sessions are ever
    open (asserted at the fake).
  - Benchmark: peak Go heap (`runtime/metrics` `/memory/classes/heap/objects:bytes`, sampled)
    stays under 200 MB for a 10 MB input.

### RUN-03 · Cancellation and graceful drain
- **M** M2 · **Status** designed · **Depends** RUN-02
- **What:** The first SIGINT or SIGTERM stops new work, lets in-flight chunks finish (15 s
  limit), writes the partial manifest (from M3, once OUT-02 exists) and exits 130. A second signal aborts immediately. The
  Codex child process is always reaped.
- **Accept:**
  - A test sends SIGINT mid-run: the exit code is 130; verified takes are present in the cache;
    no partial output file exists.
  - No orphan `codex` process remains.

### RUN-04 · Content-addressed take cache
- **M** M3 · **Status** designed · **Depends** RUN-01, VER-08
- **What:** The design §11.1 store: keyed by the canonical JSON hash of everything that affects
  the **audio**; private permissions through `internal/fsx`; atomic writes; an index with access
  times. `--no-cache` and `--refresh`. Each entry also stores its **verification record**: the
  final verdict (`accepted`, `unverified`), the verdict of every guard that ran (said, duration,
  status, language, edge-insertion, protected-token, content-word), the score, the verifier route
  and STT model, the canonicaliser and guard-set versions, and the verify mode and thresholds in
  force (R#25).
  - On reuse, the **guards required now** are computed from the current context (for example,
    the language guard is required only when the chunk's language is certain under the current
    flags). Each stored guard verdict carries a **context hash** of the inputs it judged: the
    expected language and its certainty for the language guard, the spoken form for the said,
    content-word and protected-token guards, the duration estimate and speed for the duration
    guard, the lexicon keys used for the protected-token guard. A take counts as verified only if
    every required guard has a stored `pass` verdict **whose context hash equals the hash of the
    current inputs**, the stored score meets the current accept threshold, and the canonicaliser version, guard-set
    version, verifier route and STT model all match the current ones.
  - Otherwise, if the audio itself is still valid for the key, the cached take is **re-verified in
    full**: every pre-STT guard re-runs on the stored audio, `said` text and response status,
    then STT and every post-STT guard run again. No re-render is needed unless re-verification
    rejects the take.
  - Otherwise the chunk is re-rendered. A take whose final verdict was `unverified` is never
    promoted by a policy change; it is re-verified or re-rendered.
- **Accept:**
  - The second identical run makes zero synthesizer calls (deterministic); finishing in under
    3 s for 100 chunks is a benchmark.
  - Changing one paragraph re-renders only the affected chunks.
  - Changing the voice, speed, effort, model, delivery or instructions version re-renders
    everything.
  - A take cached at score 0.96 under `--verify normal` is not reused as verified under
    `--verify strict`.
  - A take that scored 0.983 but failed the protected-token guard is never reused as verified,
    under any mode.
  - Bumping the canonicaliser version, or switching the verifier route, re-verifies cached takes
    without re-rendering them.
  - A take cached with an uncertain language and STT-detected `en`, re-run with `--lang fr`: the
    newly required language guard has no stored verdict, so the take is re-verified and rejected.
  - English audio for the literal text "Chat", cached under `--lang en` with score 1 and a
    language verdict `pass`, re-run under `--lang fr` with the same spoken text, audio key and
    verifier versions: the language verdict's context hash differs, so the take is re-verified
    and rejected, not reused.
  - A guard-set version change that makes the duration guard fail a cached take: full
    re-verification rejects it and the chunk is re-rendered.
  - Gap changes re-render and re-verify nothing.

### RUN-05 · Resume after interruption
- **M** M3 · **Status** designed · **Depends** RUN-04, RUN-03
- **What:** No extra machinery: re-running the same command after an interrupt reuses cached
  takes.
- **Accept:**
  - Interrupt at 50% → re-run → the synthesizer call count equals the remaining chunks (±
    in-flight ones); every chunk verified before the interrupt is reused byte-for-byte; the
    output order and the manifest timeline are consistent with the takes actually used. A second
    re-run with nothing changed produces a byte-identical output file.

### RUN-06 · Dry run with estimate
- **M** M3 · **Status** designed · **Depends** RUN-01, RUN-04, OBS-03
- **What:** `--dry-run` prints the chunk count, cache hits, estimated audio duration, an
  estimated wall time at the current `--jobs`, and the list-price estimate. It makes no network
  calls and needs no auth.
- **Accept:**
  - It runs without a `codex` binary and with networking disabled. `--json` emits the plan
    event.

### RUN-07 · Project and batch files
- **M** M5 · **Status** designed · **Depends** RUN-02, CFG-04, OUT-05
- **What:** `render project.toml` declares shared settings and a list of outputs, each with an
  input file and per-output overrides. Outputs render sequentially; chunks within an output in
  parallel; the cache is shared.
  ```toml
  [defaults]
  voice = "marin"
  effort = "minimal"
  format = "m4a"
  [[output]]
  input = "ch01.md"
  path = "out/01.m4a"
  [[output]]
  input = "ch02.md"
  path = "out/02.m4a"
  voice = "cedar"
  ```
- **Accept:**
  - One failed output does not stop later outputs unless `--fail-fast` is set. The exit code is
    the most severe across outputs, by the CLI-03 order. A summary table is printed.

---

## Command line (CLI)

### CLI-01 · Subcommand router and help
- **M** M0 · **Status** designed · **Depends** FND-01
- **What:** Standard library `flag`, one `FlagSet` per command, global flags parsed first. Help
  lists commands; `recite help say` gives full flag help with defaults. `say` is the default
  command: `recite "text"` and `recite -f FILE` run it. Unknown commands
  suggest the nearest one.
- **Accept:**
  - Golden help output. `recite "Hello"` and `recite say "Hello"` produce identical plans.
    `recite say check` speaks the word "check". `recite sya` suggests `say`. Flags after
    positional arguments are accepted (interspersed parsing).

### CLI-02 · `say`
- **M** M1 (grows through M5) · **Status** designed · **Depends** CLI-01, TXT-01, SPK-01, AUD-01, OUT-01
- **What:** The design §12.2 flag set, delivered incrementally: M1 brings text, voice, speed,
  model, effort and WAV. Each later flag ships with the feature that implements it
  (verification with VER-08 in M2, assembly with AUD-03 in M2, and so on). Those features depend
  on `say`, not the reverse.
- **Accept:**
  - Each flag has a test proving it reaches the component that uses it. Live M1 smoke test: `say
    "Hello" -o /tmp/h.wav` exits 0, and the test itself transcribes the file and matches it.
    From M2 onward, `say` verifies in-tool.

### CLI-03 · Exit-code contract
- **M** M1 · **Status** designed · **Depends** NET-05
- **What:** The design §12.3 table, as constants in one file, documented in `--help` and the
  README.
  - When several failures occur in one run or project, the reported code is chosen by a fixed
  severity order, independent of the order in which failures happened: 130 (interrupted) > 3
  (auth) > 4 (route denied) > 1 (internal) > 7 (network/server) > 6 (rate-limited) > 5
  (verification) > 2 (usage/input).
- **Accept:**
  - A test per code drives a fake scenario to it. Codes 0–4 and 7 are tested in M1; 5 and 6 in
    M2 (WP-2.6, WP-2.8); 130 in M2 (WP-2.8).
  - A test asserts the README table matches the constants.
  - Simultaneous auth, route-denied, verification and network failures yield 3, whatever order
    they arrive in.

### CLI-04 · `voices`
- **M** M3 · **Status** proven (E1) · **Depends** SPK-03, RUN-04
- **What:** Lists the ten voices, marking the default and OpenAI's recommended `marin` and
  `cedar`. `--preview DIR` renders the same sample sentence in every voice (cached, verified),
  with `--text` and `--effort` overrides.
- **Accept:**
  - The list needs no network. `--preview` with the fake writes 10 files, and a second run makes
    no new renders.

### CLI-05 · `doctor`
- **M** M3 · **Status** designed · **Depends** AUTH-06, AUTH-07, NET-02, VER-01, OBS-04
- **What:** The design §12.4 checks, with `--deep` for a real render and transcription. Each
  check prints `ok`, `warn` or `fail` with a fix line; `--json` is supported.
- **Accept:**
  - Each fake failure mode (no codex, API-key auth, mint 403, WebSocket refused, no ffmpeg)
    produces the right line: `fail` for the first four, and `warn` with exit 0 for no ffmpeg,
    because encoders are warn-only (design §12.4 check 6) and native WAV needs none.
    *(Amended 2026-10-11: previously "the right fail line", which contradicted §12.4.)*
  - The exit code is the most severe failing check's code.
  - With the resolved verify mode `off` (the default, D23), a `--deep` transcription failure is
    a `warn` line and does not fail doctor; with `verify.mode = "normal"` it is a `fail` line.
  - An interrupt stops doctor at once: no later check runs, so nothing is written, nothing is
    printed, and the exit is 130.
  - An omitted `ordinaryUsageAllowed` is a `warn` plan line saying the permission is unknown,
    without the denial's fix; an explicit `false` is the denial warning; every plan line keeps
    the usage windows.

### CLI-06 · JSON mode (NDJSON events)
- **M** M3 · **Status** designed · **Depends** RUN-02
- **What:** `--json` makes stdout an NDJSON event stream (design §12.5), with a documented and
  versioned event schema. Human output is suppressed.
- **Accept:**
  - Every line parses as JSON and validates against the event schema. The last line is `done`
    with the exit code.
  - A malformed global flag before the command still ends the stream with that command's final
    event. The command is found past the global flags: a flag's value (`--profile render`) is
    never taken for it.

### CLI-07 · TTY progress
- **M** M3 · **Status** designed · **Depends** RUN-02
- **What:** A single updating stderr line when stderr is a TTY (design §14.3 fields). Plain
  periodic lines otherwise. Off with `-q`.
- **Accept:**
  - No ANSI codes when stderr is not a TTY, or with `--no-color` or `NO_COLOR`.

### CLI-08 · `cache`
- **M** M4 · **Status** designed · **Depends** RUN-04
- **What:** `cache ls` (count, size, oldest, newest), `cache prune --older-than D --max-size S`,
  `cache clear` (requires `--yes` on a TTY-less run; never prompts).
- **Accept:**
  - Prune removes by access-time order to meet the size cap. `clear` without `--yes` exits 2.

### CLI-09 · `usage`
- **M** M4 · **Status** proven (account limits readable, E17) · **Depends** OBS-03, OBS-04
- **What:** Summaries from the usage ledger, `--since 7d`, grouped by day and model: renders,
  audio seconds, STT seconds and list-price estimate. Codex plan status (used %, window, reset
  time) from `account/rateLimits/read`.
- **Accept:**
  - Golden summary from a fixture ledger. Plan status is fetched from the fake app-server. No
    account identifiers are printed.

### CLI-10 · `config show / path`
- **M** M3 · **Status** designed · **Depends** CFG-04
- **What:** `config path` prints the resolved file paths. `config show` prints every effective
  setting with its source layer; `--input FILE` includes the front matter.
- **Accept:**
  - Golden output for a fixture with all six layers set for different keys.

### CLI-13 · `render`
- **M** M5 · **Status** designed · **Depends** RUN-07
- **What:** The command front-end for project files.
- **Accept:**
  - Covered by RUN-07's criteria. `render --dry-run` aggregates estimates across outputs.

### CLI-14 · `check`
- **M** M2 · **Status** designed · **Depends** CLI-01, TXT-09, TXT-10, TXT-03, TXT-14
- **What:** Runs parse → guard → spoken form → segment with no network and no auth (design §7.10).
  Prints findings (code, location, action, before/after), ambiguous spans, request-like chunks, a
  chunk plan summary, and a risk summary. Supports `--json` and `--guard strict`.
- **Accept:**
  - Works with networking disabled and no `codex` binary.
  - Exit 0 when clean, 2 when `strict` would fail.
  - The JSON validates against a published findings schema.

### CLI-15 · `prepare`
- **M** M3 · **Status** designed · **Depends** CLI-14, TXT-15
- **What:** `prepare FILE -o FILE.spoken.txt` writes the editable spoken script (design §7.10).
  `--with-findings` adds a comment per finding next to its chunk.
- **Accept:**
  - Covered by TXT-15's round-trip criteria. Output is deterministic (golden).

---

## Configuration (CFG)

### CFG-01 · Config file
- **M** M3 · **Status** designed · **Depends** `BurntSushi/toml`
- **What:** `$XDG_CONFIG_HOME/recite/config.toml` (default `~/.config/recite/config.toml`),
  using the keys of design §13. Unknown keys are an error, with the line number.
- **Accept:**
  - A typo such as `vocie` → exit 2 naming the key and line. Durations parse as Go durations
    (`250ms`).

### CFG-02 · Profiles
- **M** M3 · **Status** designed · **Depends** CFG-01
- **What:** `[profile.NAME]` tables; `--profile` or `RECITE_PROFILE` selects one. A profile
  overrides the top-level config only.
- **Accept:**
  - An unknown profile → exit 2 listing the available ones.

### CFG-03 · Environment overrides
- **M** M3 · **Status** designed · **Depends** CFG-04
- **What:** The variables listed in design §13. `OPENAI_API_KEY` is deliberately not recognised.
- **Accept:**
  - Each variable is covered by a test. Malformed values → exit 2 naming the variable.

### CFG-04 · Precedence engine
- **M** M3 · **Status** designed · **Depends** —
- **What:** Merges built-in defaults → user config → profile → project → front matter →
  environment → flags. Every value records its source for `config show`.
- **Accept:**
  - A property test: for any assignment of a key across layers, the highest layer wins.

---

## Observability (OBS)

### OBS-01 · Structured logging
- **M** M1 · **Status** designed · **Depends** —
- **What:** `log/slog` set up with `-v`/`-vv` levels, text output on stderr, and JSON when
  `--log-file` is set. The AUTH-05 redaction handler wraps everything. Network calls log class,
  path, status, latency, `x-request-id` and chunk id.
- **Accept:**
  - A golden log line for a fake mint. No header or body ever appears at any level.

### OBS-02 · Debug event dumps
- **M** M2 · **Status** designed · **Depends** NET-04, AUTH-05
- **What:** `--debug-dir DIR` writes `<chunk>-a<attempt>.events.jsonl` per render and per STT
  call, with audio payloads replaced by `<N bytes>`. On `protocol` errors the dump is written
  even without the flag, to the state directory, and its path is printed.
- **Accept:**
  - A protocol fault in the fake leaves a dump whose path appears in the error message.

### OBS-03 · Usage ledger
- **M** M3 · **Status** designed · **Depends** SPK-01, VER-01
- **What:**
  - Appends JSONL to `$XDG_STATE_HOME/recite/usage.jsonl` (design §11.3).
  - The price table carries a source date; estimates are labelled "list price, not billed".
  - The file is rotated monthly.
- **Accept:**
  - Concurrent workers never interleave partial lines (one `write` per line under `O_APPEND`,
    tested under `-race`). The estimate for a fixture matches a hand calculation.

### OBS-04 · Codex plan status
- **M** M3 · **Status** proven (E17) · **Depends** AUTH-01
- **What:** Reads `account/rateLimits/read` through the same app-server client. Exposed in
  `usage` and `doctor`. Fields: used percent, window, reset time, whether ordinary usage is
  allowed.
- **Accept:**
  - The fake's values render correctly. The call is skipped (with a warning) when the auth
    source is `file`.

---

## Release (REL)

### REL-01 · Cross-platform builds
- **M** M7 · **Status** designed · **Depends** FND-02
- **What:** `CGO_ENABLED=0` builds for darwin/arm64, darwin/amd64, linux/amd64 and linux/arm64,
  plus checksums.
- **Accept:**
  - CI produces all four binaries; each runs `version` in a matching runner or emulator.

### REL-02 · macOS signing and notarisation
- **M** M7 · **Status** designed · **Depends** REL-01
- **What:** Developer ID signing with the hardened runtime, then notarisation of a zip
  containing the binary. A bare Mach-O binary cannot be stapled, so Gatekeeper checks its ticket
  online on first run. Offline first-run is documented as a limitation. The Homebrew formula
  (REL-03) installs the same notarised binary.
- **Accept:**
  - `codesign --verify --strict --verbose=2 recite` passes, with the hardened runtime flag.
  - `xcrun notarytool info <submission>` reports `Accepted`.
  - On a clean macOS machine (or VM), the downloaded binary runs without a Gatekeeper prompt.

### REL-03 · Homebrew formula
- **M** M7 · **Status** designed · **Depends** REL-02
- **What:** A formula in the existing tap, updated by a release workflow.
- **Accept:**
  - `brew install` then `recite version` reports the released version. Upgrading never
    downgrades.

### REL-04 · User documentation and disclosure
- **M** M7 · **Status** designed · **Depends** CLI-03
- **What:** A README with quick start, every command, exit codes, configuration and the cache.
  A prominent notice says the tool relies on an undocumented use of the ChatGPT sign-in that may
  stop working, and that usage is the user's responsibility under OpenAI's terms.
- **Accept:**
  - A docs test checks that every flag in `--help` appears in the README. The notice is in the
    first screen of the README.

### REL-05 · Live release checklist
- **M** M7 · **Status** designed · **Depends** all M1–M6
- **What:** A scripted pre-release run of the live suite: `doctor --deep`; English, Chinese and
  Markdown samples; three effort levels; one compressed format; a transcription round-trip.
- **Accept:**
  - The script exits 0 before a tag is pushed, and its report is attached to the release.

---

## Rejected

| ID | Feature | Reason | Evidence |
|---|---|---|---|
| X-01 | TTS through `/v1/audio/speech` | `401 missing_scope: api.model.audio.request` for every model tried; the TTS models are deprecated (shutdown 2027-01-06) | design §2 D1 |
| X-02 | Codex app-server realtime over WebSocket | "realtime conversation requires API key auth" | E18 |
| X-03 | Codex WebRTC v3 live agent as the TTS backend | Conversational agent, not verbatim; needs WebRTC and Opus; has broken before | research agent, openai/codex #40140 |
| X-04 | Any API-key backend or fallback | Violates G1 | project goal |
| X-05 | `whisper-1` for verification | `401 missing_scope` | E15 |
| X-06 | Refreshing tokens inside recite | Rotates the refresh token and can break Codex; refresh is delegated to Codex (AUTH-04) | design D2 |
| X-07 | Seeded or deterministic audio | No `seed` in the realtime API; the cache (RUN-04) is the substitute | API reference |
| X-08 | Custom or cloned voices | Sales-gated; not offered on this route | voice docs |
| X-09 | Word-level subtitles and karaoke timing | `gpt-transcribe` rejects `verbose_json`; no word timestamps on any accepted model | E15 |
| X-10 | Prompting the verifier with the full chunk text | Biases the recogniser toward the expected words and hides real defects; keywords only (VER-10) | design D5 |
| X-11 | Exposing the undocumented effort value `none` | Accepted today but undocumented; no measured benefit over `minimal` | E21 |
| X-12 | Trusting the model's own transcript as verification | Matched perfectly on takes that independent STT showed to differ | E5 |
| X-13 | OpenAI's JSON verbatim wrapper as the prompt | The model read the JSON aloud: 5/16 verbatim on `2.1`, 9/16 on `2` | E22 |
| X-14 | A numeric pitch control | No API parameter. Only soft delivery words, which are covered by SPK-07. | API reference |
| AUD-09 | Decoding arbitrary input audio | Only served the speech-input commands; TTS-only scope (owner's decision) | — |
| CLI-11 | `transcribe` command | TTS-only scope (owner's decision). Internal verification still transcribes takes (VER-01). | — |
| CLI-12 | `translate` command (`gpt-realtime-translate`) | TTS-only scope (owner's decision) | — |
| X-15 | Automatic typo correction | Silently changes the author's words | design §7.6 |
| INT-01 | Go library API (`pkg/recite`) | Programs use the CLI, whose `--json` events and exit codes are documented; a second API would duplicate it (owner's decision) | design D21 |
| INT-02 | MCP server (`recite mcp`) | Agents can run the CLI; a second entry point would need its own path containment (owner's decision) | design D21 |
| INT-03 | Translate text before speaking, via Codex | Translation belongs before recite, with any tool; it needed an isolated Codex home the design never solved (owner's decision) | design D21 |
| INT-04 | Codex-assisted proofreading and ambiguity report | Proofreading belongs before recite, with any tool; same unsolved isolated home. `G-TYPO`, its only hook, is removed (owner's decision) | design D21 |
