BEGIN;

-- Persist the canonical outlet name consumed by query and Agent authorization.
-- Older model versions used `external` for the Agent capability.
UPDATE model.resource_model_versions
SET policy = (policy #- '{outlets,external}') || jsonb_build_object(
    'outlets',
    (policy -> 'outlets') || jsonb_build_object('agent_tool', policy #> '{outlets,external}')
)
WHERE policy #> '{outlets,external}' IS NOT NULL
  AND policy #> '{outlets,agent_tool}' IS NULL;

COMMIT;
