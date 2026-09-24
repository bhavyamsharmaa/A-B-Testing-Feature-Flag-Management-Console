package evaluation

import "hash/fnv"

// TotalBuckets matches the PRD's basis-point convention (US-04 AC-1):
// splits and rollouts are integer weights summing to 100000.
const TotalBuckets = 100_000

// Bucket deterministically maps a (flag, salt, subject) triple to an integer
// in [0, TotalBuckets). It must stay a pure function of its inputs — never
// rand() — so a user's assignment is identical on every call (US-03 AC-2).
// The salt is per flag+environment, so two flags at 50% assign the same
// user independently (US-03 AC-4).
func Bucket(flagKey, salt, subjectKey string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(flagKey))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(salt))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(subjectKey))
	return h.Sum32() % TotalBuckets
}

type VariationWeight struct {
	VariationID string
	BasisPoints uint32
}

// AssignVariation returns the variation whose cumulative range contains
// bucket. weights must come in a fixed order (the flag's variation order):
// iterating a map here would reshuffle the ranges between calls and move
// users between variations.
//
// Raising the first variation's share never moves anyone already in it
// (monotonic rollout, US-03 AC-3); lowering it does, which is why the
// console must warn before that save.
func AssignVariation(bucket uint32, weights []VariationWeight) (string, bool) {
	var cumulative uint32
	for _, w := range weights {
		cumulative += w.BasisPoints
		if bucket < cumulative {
			return w.VariationID, true
		}
	}
	return "", false
}
