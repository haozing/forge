-- 0023_note_approval_policy.sql
-- The inspiration→knowledge-base flow must go through administrator review
-- with no bypass (product decision 2026-09-04 v1.2 #2): builtin_note flips
-- from direct publishing to approval-gated publishing. The policy is edited
-- in place on version 1 — builtin seed models are re-seeded identically on
-- bootstrap, and frozen asset-version references keep pointing at a policy
-- that now means "review required".

UPDATE model.resource_model_versions v
SET policy = jsonb_set(policy, '{publishing,mode}', '"approval"'::jsonb)
FROM model.resource_models m
WHERE m.id = v.resource_model_id
  AND m.model_key = 'builtin_note'
  AND v.version_no = 1
  AND policy->'publishing'->>'mode' = 'direct';
