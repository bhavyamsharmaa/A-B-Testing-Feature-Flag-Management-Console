package segments

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"helios/backend/internal/controlplane/flags"
	"helios/backend/internal/controlplane/quota"
	"helios/backend/internal/dataplane/evaluation"
)

const (
	maxNameRunes        = 120
	maxDescriptionRunes = 1000
	// MaxSubjectKeyRunes matches the CHECK on segment_members.subject_key.
	MaxSubjectKeyRunes = 256
	// maxBodyBytes caps a segment write: 10,000 members of 256 characters is
	// about 2.6 MB of JSON.
	maxBodyBytes = 4 << 20
)

// validationError is a 400 INVALID_REQUEST.
type validationError struct{ msg string }

func (e validationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return validationError{fmt.Sprintf(format, args...)}
}

func validateKey(key string) error {
	if !flags.ValidKey(key) {
		return invalid("key must be 1-128 characters of letters, digits, '_', '.', '-', starting with a letter or digit")
	}
	return nil
}

func validateName(name string) error {
	if n := utf8.RuneCountInString(name); n == 0 || n > maxNameRunes {
		return invalid("name must be 1-%d characters", maxNameRunes)
	}
	return nil
}

func validateDescription(d string) error {
	if utf8.RuneCountInString(d) > maxDescriptionRunes {
		return invalid("description must be at most %d characters", maxDescriptionRunes)
	}
	return nil
}

// validateRules checks a segment's attribute rules: each clause is a valid
// clause of the flag-rule operators, none references a segment (so segments
// cannot nest and no cycle can exist), and the definition is within the
// quota sizes. The sizes are reported as quota.Exceeded (409), the clause
// problems as validation errors (400).
func validateRules(rules []evaluation.Clause) error {
	for i, c := range rules {
		if err := c.Validate(evaluation.SegmentRuleClause); err != nil {
			return invalid("rules[%d]: %v", i, err)
		}
	}
	encoded, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	return quota.CheckSegmentRules(len(encoded), len(rules))
}

// cleanMembers validates and de-duplicates explicit member subject keys,
// keeping first-seen order. A key is a non-empty string of at most 256
// characters without control characters (a NUL byte cannot be stored in
// Postgres text, and a newline in a key is always a paste mistake). Keys are
// exact: no trimming or case folding, because subject keys are opaque ids.
// More distinct keys than the quota allows is quota.Exceeded.
func cleanMembers(keys []string) ([]string, error) {
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for i, k := range keys {
		if k == "" {
			return nil, invalid("members[%d]: a member key cannot be empty", i)
		}
		if !utf8.ValidString(k) || utf8.RuneCountInString(k) > MaxSubjectKeyRunes {
			return nil, invalid("members[%d]: a member key must be valid text of at most %d characters", i, MaxSubjectKeyRunes)
		}
		for _, r := range k {
			if r < 0x20 || r == 0x7f {
				return nil, invalid("members[%d]: a member key cannot contain control characters (line breaks, tabs, NUL)", i)
			}
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
		if err := quota.CheckSegmentMembers(len(out)); err != nil {
			return nil, err
		}
	}
	return out, nil
}
