package cache

// Decision is what a run does with a cached take (design §11.1).
type Decision int

// Decisions, from cheapest to dearest.
const (
	// ReuseVerified: the stored verification still holds; no STT and no render.
	ReuseVerified Decision = iota + 1
	// ReuseUnverified: an unverified take kept as is under --allow-unverified.
	ReuseUnverified
	// Reverify: the audio is valid for its key but the verdict must be judged again in full.
	Reverify
)

// Current is the verification context of the run now.
type Current struct {
	Accept          float64 // the accept threshold of the current mode
	Route, STTModel string
	CanonVersion    int
	GuardVersion    int
	// Required maps each guard required now to the hash of the inputs it would judge.
	Required        map[string]string
	AllowUnverified bool
}

// Decide re-judges a stored verdict against the current context (RUN-04). A take counts as
// verified only if every required guard has a stored pass whose context hash equals the
// current one, the stored score meets the current threshold, and the canonicaliser version,
// guard version, verifier route and STT model all match. An unverified take is never promoted:
// it is re-verified, or kept unverified when the run allows unverified output.
func (v Verification) Decide(cur Current) Decision {
	if v.Verdict != VerdictAccepted {
		if cur.AllowUnverified {
			return ReuseUnverified
		}
		return Reverify
	}
	if v.CanonVersion != cur.CanonVersion || v.GuardVersion != cur.GuardVersion ||
		v.Route != cur.Route || v.STTModel != cur.STTModel || !(v.Score >= cur.Accept) {
		return Reverify
	}
	for name, hash := range cur.Required {
		g, ok := v.Guards[name]
		if !ok || g.Verdict != GuardPass || g.ContextHash != hash {
			return Reverify
		}
	}
	return ReuseVerified
}
