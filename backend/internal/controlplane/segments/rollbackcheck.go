package segments

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UsesNewFeaturesSQL lists, per workspace, the flag configurations (flag in
// environment) whose targeting rules use something an evaluator older than
// segments does not understand: a segment clause, one of the operators added
// with segments, or the negate flag. Such an evaluator treats an operator it does not know as "no
// match", so those rules would silently stop matching if the backend were
// rolled back. The five operators it does know are listed; everything else counts.
//
// It is read-only and returns identifiers only: workspace id, flag key,
// environment key, and the names of the features used. No member data, no
// rule values.
//
// The same text is in docs/SEGMENTS.md; TestUsesNewFeaturesSQL keeps the list
// of old operators in step with the evaluator.
const UsesNewFeaturesSQL = `
WITH clauses AS (
    SELECT fc.id AS config_id, c
    FROM flag_configs fc,
         jsonb_array_elements(fc.targeting_rules) AS r,
         jsonb_array_elements(CASE WHEN jsonb_typeof(r -> 'clauses') = 'array' THEN r -> 'clauses' ELSE '[]'::jsonb END) AS c
), features AS (
    SELECT config_id, c ->> 'operator' AS feature FROM clauses
    WHERE c ->> 'operator' NOT IN ('equals', 'in', 'contains', 'greaterThan', 'exists')
    UNION
    SELECT config_id, 'negate' FROM clauses WHERE c -> 'negate' = 'true'::jsonb
)
SELECT fc.workspace_id::text, f.key, e.key, array_agg(DISTINCT ft.feature ORDER BY ft.feature)
FROM features ft
JOIN flag_configs fc ON fc.id = ft.config_id
JOIN flags f ON f.id = fc.flag_id AND f.workspace_id = fc.workspace_id
JOIN environments e ON e.id = fc.environment_id AND e.workspace_id = fc.workspace_id
GROUP BY fc.workspace_id, f.key, e.key
ORDER BY fc.workspace_id, f.key, e.key`

// FlagUse is one flag configuration that uses the new features.
type FlagUse struct {
	WorkspaceID string
	FlagKey     string
	Environment string
	Features    []string
}

// FlagsUsingNewFeatures runs UsesNewFeaturesSQL in a read-only transaction.
func FlagsUsingNewFeatures(ctx context.Context, pool *pgxpool.Pool) ([]FlagUse, error) {
	var out []FlagUse
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, UsesNewFeaturesSQL)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (FlagUse, error) {
			var u FlagUse
			return u, r.Scan(&u.WorkspaceID, &u.FlagKey, &u.Environment, &u.Features)
		})
		return err
	})
	return out, err
}
