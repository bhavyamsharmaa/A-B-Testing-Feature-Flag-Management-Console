package evaluation

import "hash/fnv"

// TotalBuckets matches the PRD's basis-point convention (US-04 AC-1):
// splits and rollouts are integer weights summing to 100000.
const TotalBuckets = 100_000

// Bucket deterministically maps a (flag, salt, subject) triple to an integer
// in [0, TotalBuckets). It MUST be a pure function of its inputs — never
// rand() — so that a user's assignment is identical on every call, forever
// (US-03 AC-2). The salt is per flag+environment so two flags at 50% each
// produce statistically independent assignments for the same user (AC-4).
func Bucket(flagKey, salt, subjectKey string) uint32 {
	h := fnv.New32a()
	// error from Write is always nil for fnv; ignored deliberately
	_, _ = h.Write([]byte(flagKey))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(salt))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(subjectKey))
	return h.Sum32() % TotalBuckets
}

// InRollout reports whether a bucket falls within a percentage rollout,
// expressed in basis points (e.g. 10% = 10000). Rollouts are monotonic by
// construction here: raising percentage never revokes a bucket already
// included, because bucket membership is "bucket < percentageBasisPoints"
// over the same fixed hash space (US-03 AC-3). Lowering the percentage DOES
// revoke access for buckets that fall outside the new, smaller range — the
// caller (control-plane API) is responsible for warning before that save.
func InRollout(bucket uint32, percentageBasisPoints uint32) bool {
	return bucket < percentageBasisPoints
}

// AssignVariation walks weighted variations (integer basis points summing to
// TotalBuckets) and returns the variation whose cumulative range contains
// bucket. Used for experiment traffic allocation (US-04) and multivariate
// rollouts alike.
func AssignVariation(bucket uint32, weights []VariationWeight) (variationID string, ok bool) {
	var cumulative uint32
	for _, w := range weights {
		cumulative += w.BasisPoints
		if bucket < cumulative {
			return w.VariationID, true
		}
	}
	return "", false
}

type VariationWeight struct {
	VariationID string
	BasisPoints uint32
}
