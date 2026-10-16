//go:build race

package canon

// Under the race detector TestEquivalentWorstCase reads texts raceFactor times shorter. The randomized
// property tests run raceFactor times fewer cases: canon starts no goroutine, so instrumenting it finds nothing and
// only costs time, and make check (FND-03) must finish within five minutes. The full counts run without the race
// detector, in plain go test and in the scheduled CI job.
const raceFactor = 8
