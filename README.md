# recite

recite is a Go CLI that turns text into verified speech using the local Codex ChatGPT sign-in.

Work in progress.

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
