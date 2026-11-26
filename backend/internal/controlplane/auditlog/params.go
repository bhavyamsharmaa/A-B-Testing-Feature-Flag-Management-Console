package auditlog

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultLimit     = 50
	maxLimit         = 200
	maxResourceIDLen = 256
)

// action and resourceType are short identifiers like "flag.kill" and "flag".
var tokenRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// listParams are the validated query parameters of GET .../audit-logs. A zero
// value for a filter means "not filtered".
type listParams struct {
	Limit        int
	Before       int64 // return rows with id < Before
	Action       string
	ResourceType string
	ResourceID   string
	Severity     string
}

// parseListParams validates the query string. Every error is safe to show to
// the caller as a 400 message.
func parseListParams(q url.Values) (listParams, error) {
	p := listParams{Limit: defaultLimit}

	if v, ok, err := single(q, "limit"); err != nil {
		return p, err
	} else if ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return p, fmt.Errorf("limit must be an integer of at least 1 (values above %d are clamped)", maxLimit)
		}
		p.Limit = min(n, maxLimit)
	}

	if v, ok, err := single(q, "before"); err != nil {
		return p, err
	} else if ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return p, fmt.Errorf("before must be a positive integer (an entry id)")
		}
		p.Before = n
	}

	if v, ok, err := single(q, "severity"); err != nil {
		return p, err
	} else if ok {
		if v != "info" && v != "critical" {
			return p, fmt.Errorf("severity must be info or critical")
		}
		p.Severity = v
	}

	for _, f := range []struct {
		name string
		dst  *string
	}{{"action", &p.Action}, {"resourceType", &p.ResourceType}} {
		if v, ok, err := single(q, f.name); err != nil {
			return p, err
		} else if ok {
			if !tokenRE.MatchString(v) {
				return p, fmt.Errorf("%s must be 1-64 characters of letters, digits, '_', '.', '-'", f.name)
			}
			*f.dst = v
		}
	}

	if v, ok, err := single(q, "resourceId"); err != nil {
		return p, err
	} else if ok {
		// Postgres rejects NUL bytes and invalid UTF-8 in text, which would
		// otherwise surface as a 500.
		if v == "" || len(v) > maxResourceIDLen || !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			return p, fmt.Errorf("resourceId must be 1-%d characters of valid text", maxResourceIDLen)
		}
		p.ResourceID = v
	}

	return p, nil
}

// single returns the one value of a parameter. A repeated parameter is
// ambiguous, so it is an error rather than "first wins".
func single(q url.Values, name string) (value string, present bool, err error) {
	vals, ok := q[name]
	if !ok {
		return "", false, nil
	}
	if len(vals) > 1 {
		return "", false, fmt.Errorf("%s was given more than once", name)
	}
	return vals[0], true, nil
}

// buildListQuery returns parameterized SQL and its arguments. Filter values
// only ever travel as arguments, never as part of the SQL text. It selects
// Limit+1 rows so the handler can tell whether another page exists.
//
// Entries with no environment are included in every environment's log and
// marked scope = 'global'. Today that is flag.create: a flag is created in
// every environment at once, so its audit entry belongs to none of them.
func buildListQuery(environmentID string, p listParams) (string, []any) {
	args := []any{environmentID}
	where := []string{"(environment_id = $1::uuid OR environment_id IS NULL)"}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if p.Before > 0 {
		add("id < $%d", p.Before)
	}
	if p.Action != "" {
		add("action = $%d", p.Action)
	}
	if p.ResourceType != "" {
		add("resource_type = $%d", p.ResourceType)
	}
	if p.ResourceID != "" {
		add("resource_id = $%d", p.ResourceID)
	}
	if p.Severity != "" {
		add("severity = $%d", p.Severity)
	}
	args = append(args, p.Limit+1)

	// JSONB is read as ::text, like the rest of the backend, so scanning works
	// under both the simple and extended query protocols.
	query := `
	SELECT id, created_at, actor_email, action, resource_type, resource_id, severity,
	       COALESCE(diff_before::text, 'null'), COALESCE(diff_after::text, 'null'),
	       CASE WHEN environment_id IS NULL THEN 'global' ELSE 'environment' END
	FROM audit_logs
	WHERE ` + strings.Join(where, " AND ") + fmt.Sprintf(`
	ORDER BY id DESC
	LIMIT $%d`, len(args))
	return query, args
}
