package evaluation

import (
	"fmt"
	"regexp"
	"sync"
	"unicode/utf8"
)

// Regex safety. Patterns are compiled with Go's standard regexp package, which
// is RE2: matching runs in time linear in the input, with no backtracking, so
// no pattern can take exponential time. On top of that, the pattern and the
// input are length-capped, patterns are validated when a rule is saved, and
// a pattern that fails to compile at evaluation time (it cannot, for a saved
// rule) is simply a non-match.
const (
	// MaxRegexPatternBytes is the longest accepted pattern.
	MaxRegexPatternBytes = 256
	// MaxRegexInputBytes is the longest attribute value a regex is run on. A
	// longer value is a non-match (never an error), negated or not.
	MaxRegexInputBytes = 2048
)

// ValidateRegex reports why pattern cannot be used, or nil.
func ValidateRegex(pattern string) error {
	if pattern == "" {
		return fmt.Errorf("a regular expression is required")
	}
	if len(pattern) > MaxRegexPatternBytes {
		return fmt.Errorf("regular expression is longer than %d bytes", MaxRegexPatternBytes)
	}
	if !utf8.ValidString(pattern) {
		return fmt.Errorf("regular expression is not valid UTF-8")
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return fmt.Errorf("invalid regular expression: %v", err)
	}
	return nil
}

// regexCache keeps compiled patterns so a hot rule is not recompiled on every
// evaluation. It is bounded; when full it is emptied (patterns are cheap to
// compile, and a saved rule set is small).
var regexCache = struct {
	mu sync.Mutex
	m  map[string]*regexp.Regexp // nil value: known not to compile
}{m: map[string]*regexp.Regexp{}}

const regexCacheMax = 512

func compiledRegex(pattern string) *regexp.Regexp {
	regexCache.mu.Lock()
	re, hit := regexCache.m[pattern]
	regexCache.mu.Unlock()
	if hit {
		return re
	}
	if len(pattern) <= MaxRegexPatternBytes {
		re, _ = regexp.Compile(pattern) // nil on error
	}
	regexCache.mu.Lock()
	if len(regexCache.m) >= regexCacheMax {
		regexCache.m = map[string]*regexp.Regexp{}
	}
	regexCache.m[pattern] = re
	regexCache.mu.Unlock()
	return re
}

// regexMatch reports (matched, determinate). It is indeterminate when the
// input is too long or the pattern unusable.
func regexMatch(pattern, input string) (bool, bool) {
	if len(input) > MaxRegexInputBytes {
		return false, false
	}
	re := compiledRegex(pattern)
	if re == nil {
		return false, false
	}
	return re.MatchString(input), true
}
