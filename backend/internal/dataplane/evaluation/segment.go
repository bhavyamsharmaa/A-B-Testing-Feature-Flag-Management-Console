package evaluation

// MemberSet answers "is this subject an explicit member?" in constant (map) or
// logarithmic (indexed lookup) time. Evaluation never scans a member list.
type MemberSet interface {
	Has(subjectKey string) bool
}

// MapMembers is an in-memory MemberSet (a hash set: O(1) lookup), for tests
// and for an in-process SDK that holds whole segments.
type MapMembers map[string]struct{}

func (m MapMembers) Has(subjectKey string) bool {
	_, ok := m[subjectKey]
	return ok
}

// SubjectMember is the MemberSet /evaluate uses: the database has already
// answered, by an indexed lookup, whether the one subject of this request is
// in the segment, so no member list is ever loaded.
type SubjectMember struct {
	Subject string
	In      bool
}

func (m SubjectMember) Has(subjectKey string) bool { return m.In && subjectKey == m.Subject }

// Segment is a reusable audience: a subject is in it if it is an explicit
// member OR matches Rules. Rules are ANDed, like the clauses of a flag rule;
// a segment with no rules contains only its explicit members. Segment rules
// cannot reference other segments (validation rejects it), so evaluation
// never recurses.
type Segment struct {
	Key     string
	Members MemberSet
	Rules   []Clause
}

// Contains reports whether the subject (with its attributes) is in the segment.
func (s Segment) Contains(subjectKey string, attrs map[string]any) bool {
	if s.Members != nil && subjectKey != "" && s.Members.Has(subjectKey) {
		return true
	}
	if len(s.Rules) == 0 {
		return false
	}
	env := matchEnv{subject: subjectKey, attrs: attrs} // no segments: nesting is impossible
	for _, c := range s.Rules {
		if !c.matches(env) {
			return false
		}
	}
	return true
}
