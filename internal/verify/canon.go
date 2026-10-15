package verify

import "github.com/xiaolai/sayso/go/canon"

// The canonicaliser lives in github.com/xiaolai/sayso/go/canon; the scorer reads text through it.

// CanonVersion identifies the canonicaliser's behaviour. Cache keys and verification provenance
// record it.
const CanonVersion = canon.CanonVersion

// Canon canonicalises s into comparison tokens (design §8.3).
func Canon(s string) []string { return canon.Canon(s) }

// Equivalent reports whether a and b canonicalise equal, also trying hyphenated compounds
// joined and years read as pairs.
func Equivalent(a, b string) bool { return canon.Equivalent(a, b) }

// The canonicaliser's rules the scorer and the guards share.
var (
	yearPairAt   = canon.YearPairAt
	cardinal     = canon.Cardinal
	isNumberWord = canon.IsNumberWord
	isScale      = canon.IsScale
	isHanNumeral = canon.IsHanNumeral
	isHyphen     = canon.IsHyphen
	isApostrophe = canon.IsApostrophe
)
