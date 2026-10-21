-- A SYNTHETIC "old production": the schema of main (6c90844: migrations 0001-0003) filled with made-up data, to rehearse
-- release 1 when no real dump is available. Nothing here is real: users are @synthetic.invalid, ids are fixed so reports
-- can name them. Run on an EMPTY local database after 0001-0003 (see build_synthetic.sh). Sizes: 60 flags x 5 environments,
-- 6 experiments, 10 users, 250,000 audit rows, 250,000 event rows.
--
-- The users model the OLD (per-environment role) world:
--   alice  admin in every environment      -> becomes the owner of the legacy workspace (0004 requires such a user)
--   bob    admin in dev only               carol  approver everywhere        dan   editor in dev and staging
--   erin   viewer in dev                   frank  editor everywhere          jack  viewer in production only
--   gina, hank, irene: signed up, have NO role anywhere (never invited)
INSERT INTO auth.users (id, email) VALUES
  ('aaaaaaaa-0000-4000-8000-000000000001', 'alice@synthetic.invalid'), ('aaaaaaaa-0000-4000-8000-000000000002', 'bob@synthetic.invalid'),
  ('aaaaaaaa-0000-4000-8000-000000000003', 'carol@synthetic.invalid'), ('aaaaaaaa-0000-4000-8000-000000000004', 'dan@synthetic.invalid'),
  ('aaaaaaaa-0000-4000-8000-000000000005', 'erin@synthetic.invalid'),  ('aaaaaaaa-0000-4000-8000-000000000006', 'frank@synthetic.invalid'),
  ('aaaaaaaa-0000-4000-8000-000000000007', 'gina@synthetic.invalid'),  ('aaaaaaaa-0000-4000-8000-000000000008', 'hank@synthetic.invalid'),
  ('aaaaaaaa-0000-4000-8000-000000000009', 'irene@synthetic.invalid'), ('aaaaaaaa-0000-4000-8000-000000000010', 'jack@synthetic.invalid');

INSERT INTO environments (key, name, is_production) VALUES ('qa', 'QA', false), ('sandbox', 'Sandbox', false);

INSERT INTO user_environment_roles (user_id, environment_id, role)
  SELECT 'aaaaaaaa-0000-4000-8000-000000000001', id, 'admin' FROM environments;
INSERT INTO user_environment_roles (user_id, environment_id, role)
  SELECT 'aaaaaaaa-0000-4000-8000-000000000002', id, 'admin' FROM environments WHERE key = 'dev';
INSERT INTO user_environment_roles (user_id, environment_id, role)
  SELECT 'aaaaaaaa-0000-4000-8000-000000000003', id, 'approver' FROM environments;
INSERT INTO user_environment_roles (user_id, environment_id, role)
  SELECT 'aaaaaaaa-0000-4000-8000-000000000004', id, 'editor' FROM environments WHERE key IN ('dev', 'staging');
INSERT INTO user_environment_roles (user_id, environment_id, role)
  SELECT 'aaaaaaaa-0000-4000-8000-000000000005', id, 'viewer' FROM environments WHERE key = 'dev';
INSERT INTO user_environment_roles (user_id, environment_id, role)
  SELECT 'aaaaaaaa-0000-4000-8000-000000000006', id, 'editor' FROM environments;
INSERT INTO user_environment_roles (user_id, environment_id, role)
  SELECT 'aaaaaaaa-0000-4000-8000-000000000010', id, 'viewer' FROM environments WHERE key = 'production';

INSERT INTO flags (key, name, description, variation_type, variations, created_by)
  SELECT 'flag-' || lpad(g::text, 3, '0'), 'Flag ' || g, 'synthetic', 'boolean',
         '[{"id":"on","value":true},{"id":"off","value":false}]'::jsonb,
         ('aaaaaaaa-0000-4000-8000-0000000000' || lpad((CASE WHEN g % 3 = 0 THEN 1 WHEN g % 3 = 1 THEN 4 ELSE 6 END)::text, 2, '0'))::uuid
  FROM generate_series(1, 60) g;
INSERT INTO flag_configs (flag_id, environment_id, enabled, targeting_rules, rollout, salt, fallthrough_variation_id)
  SELECT f.id, e.id, (right(f.key, 1)::int % 2 = 0),
         CASE WHEN right(f.key, 1) = '3' THEN '[{"clauses":[{"attribute":"plan","operator":"equals","value":"pro"}],"variationId":"on"}]'::jsonb ELSE '[]'::jsonb END,
         CASE WHEN right(f.key, 1) = '5' THEN '{"on":30000,"off":70000}'::jsonb END,
         md5(f.key || e.key), 'off'
  FROM flags f CROSS JOIN environments e;

-- 6 experiments: 2 draft, 2 running, 2 stopped, in dev, each with a primary metric
INSERT INTO experiments (environment_id, flag_id, key, name, status, started_at, stopped_at, start_config, stop_config, created_by)
  SELECT e.id, f.id, 'exp-' || n, 'Experiment ' || n,
         (CASE WHEN n <= 2 THEN 'draft' WHEN n <= 4 THEN 'running' ELSE 'stopped' END)::experiment_status,
         CASE WHEN n > 2 THEN now() - interval '10 days' END, CASE WHEN n > 4 THEN now() - interval '2 days' END,
         CASE WHEN n > 2 THEN '{"version":1}'::jsonb END, CASE WHEN n > 4 THEN '{"version":1}'::jsonb END,
         'aaaaaaaa-0000-4000-8000-000000000001'
  FROM generate_series(1, 6) n
  JOIN environments e ON e.key = 'dev' JOIN flags f ON f.key = 'flag-' || lpad(n::text, 3, '0');
INSERT INTO experiment_metrics (experiment_id, name, event_name, type, is_primary)
  SELECT id, 'Conversions', 'purchase', 'conversion', true FROM experiments;

-- 250,000 audit rows (a third without an environment, like workspace-level actions) and 250,000 events
INSERT INTO audit_logs (actor_id, actor_email, environment_id, action, resource_type, resource_id, severity, diff_after, created_at)
  SELECT ('aaaaaaaa-0000-4000-8000-0000000000' || lpad((1 + g % 6)::text, 2, '0'))::uuid, 'user' || (1 + g % 6) || '@synthetic.invalid',
         CASE WHEN g % 3 = 0 THEN NULL ELSE (SELECT id FROM environments ORDER BY key OFFSET (g % 5) LIMIT 1) END,
         'flag.update', 'flag', 'flag-' || lpad((1 + g % 60)::text, 3, '0'), CASE WHEN g % 50 = 0 THEN 'critical' ELSE 'info' END,
         jsonb_build_object('enabled', g % 2 = 0), now() - (g || ' seconds')::interval
  FROM generate_series(1, 250000) g;
INSERT INTO events (environment_id, experiment_id, subject_key, variation_id, event_name, value, dedup, created_at)
  SELECT x.environment_id, x.id, 'subject-' || g, CASE WHEN g % 2 = 0 THEN 'on' ELSE 'off' END,
         'revenue', (g % 100)::float8, false, now() - (g || ' seconds')::interval
  FROM generate_series(1, 250000) g JOIN experiments x ON x.key = 'exp-' || (3 + g % 2);
