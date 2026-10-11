// Package exitcode is the single source of the process exit codes (design §12.3).
package exitcode

// Exit codes of the CLI contract (design §12.3).
const (
	OK           = 0
	Internal     = 1   // a bug
	Usage        = 2   // bad flag, bad input, missing encoder, unknown voice
	Auth         = 3   // not signed in, not a ChatGPT sign-in, refresh failed
	RouteDenied  = 4   // the token was refused for this model or endpoint
	VerifyFailed = 5   // a chunk failed verification after all attempts
	RateLimited  = 6   // rate-limited or quota exhausted after backoff
	Failure      = 7   // network, server or protocol failure after retries
	Interrupted  = 130 // SIGINT/SIGTERM
)

// Meanings documents every code, in code order; `--help` and the README table
// are generated from or checked against it (CLI-03).
var Meanings = []struct {
	Code    int
	Meaning string
}{
	{OK, "Success"},
	{Internal, "Internal error (bug)"},
	{Usage, "Usage or input error: bad flag, bad input encoding, missing encoder, unknown voice"},
	{Auth, "Auth: not signed in, not a ChatGPT sign-in, refresh failed"},
	{RouteDenied, "Route denied: the token was refused for this model or endpoint"},
	{VerifyFailed, "Verification failed for at least one chunk after all attempts"},
	{RateLimited, "Rate-limited or quota exhausted after backoff"},
	{Failure, "Network, server or protocol failure after retries"},
	{Interrupted, "Interrupted"},
}

// severity ranks codes for Worst: 130 > 3 > 4 > 1 > 7 > 6 > 5 > 2 (> 0).
var severity = map[int]int{
	Interrupted:  8,
	Auth:         7,
	RouteDenied:  6,
	Internal:     5,
	Failure:      4,
	RateLimited:  3,
	VerifyFailed: 2,
	Usage:        1,
	OK:           0,
}

// Worst returns the code to report when several failures occurred, following the
// fixed severity order so the result does not depend on arrival order (R#23).
// An unknown code is a programming error and is reported as Internal.
func Worst(codes ...int) int {
	worst := OK
	for _, c := range codes {
		if _, ok := severity[c]; !ok {
			c = Internal
		}
		if severity[c] > severity[worst] {
			worst = c
		}
	}
	return worst
}
