BEGIN;

UPDATE model.resource_model_versions
SET policy = policy #- '{outlets,external}'
WHERE policy #> '{outlets,external}' IS NOT NULL
  AND policy #> '{outlets,agent_tool}' IS NOT NULL;

UPDATE model.resource_model_versions
SET policy = (policy #- '{outlets,open_api}') || jsonb_build_object(
    'outlets',
    (policy -> 'outlets') || jsonb_build_object('agent_tool', policy #> '{outlets,open_api}')
)
WHERE policy #> '{outlets,open_api}' IS NOT NULL
  AND policy #> '{outlets,agent_tool}' IS NULL;

UPDATE model.resource_model_versions
SET policy = policy #- '{outlets,open_api}'
WHERE policy #> '{outlets,open_api}' IS NOT NULL
  AND policy #> '{outlets,agent_tool}' IS NOT NULL;

UPDATE model.resource_model_versions
SET policy = (policy #- '{outlets,member_search}') || jsonb_build_object(
    'outlets',
    (policy -> 'outlets') || jsonb_build_object('workspace', policy #> '{outlets,member_search}')
)
WHERE policy #> '{outlets,member_search}' IS NOT NULL
  AND policy #> '{outlets,workspace}' IS NULL;

UPDATE model.resource_model_versions
SET policy = policy #- '{outlets,member_search}'
WHERE policy #> '{outlets,member_search}' IS NOT NULL
  AND policy #> '{outlets,workspace}' IS NOT NULL;

COMMIT;
