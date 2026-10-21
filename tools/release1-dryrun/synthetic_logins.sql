-- Rehearsal only: give the synthetic people their real-looking emails (the restored copy only has placeholder ones) and add the
-- three who never held a role (they are in Supabase but not in the dump). Run BEFORE the migrations: 0004 names the legacy
-- workspace after its owner's email at that moment.
UPDATE auth.users u SET email = n.name || '@synthetic.invalid' FROM (VALUES
  ('aaaaaaaa-0000-4000-8000-000000000001'::uuid,'alice'),('aaaaaaaa-0000-4000-8000-000000000002','bob'),('aaaaaaaa-0000-4000-8000-000000000003','carol'),
  ('aaaaaaaa-0000-4000-8000-000000000004','dan'),('aaaaaaaa-0000-4000-8000-000000000005','erin'),('aaaaaaaa-0000-4000-8000-000000000006','frank'),
  ('aaaaaaaa-0000-4000-8000-000000000010','jack')) n(id, name) WHERE u.id = n.id;
INSERT INTO auth.users (id, email) VALUES
  ('aaaaaaaa-0000-4000-8000-000000000007','gina@synthetic.invalid'),('aaaaaaaa-0000-4000-8000-000000000008','hank@synthetic.invalid'),
  ('aaaaaaaa-0000-4000-8000-000000000009','irene@synthetic.invalid') ON CONFLICT DO NOTHING;
