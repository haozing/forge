-- 0012_builtin_resource_models.sql
-- Deterministic, idempotent baseline seed: one active builtin model plus one
-- published initial version per organization, using the final
-- visibility/channels/retrieval/publishing policy structure. Runtime
-- bootstrap re-applies the same inserts for organizations created after this
-- migration (store.SeedBuiltinResourceModels).
BEGIN;

INSERT INTO model.resource_models
    (organization_id, model_key, name, description, content_kind, status, created_by)
SELECT o.id,
       d.model_key,
       d.name,
       d.description,
       d.content_kind,
       'active',
       a.id
FROM organization.organizations o
JOIN LATERAL (
    SELECT u.id
    FROM identity.users u
    WHERE u.organization_id = o.id
      AND u.organization_role = 'admin'
      AND u.user_type = 'member'
      AND u.status = 'active'
    ORDER BY u.created_at, u.id
    LIMIT 1
) a ON TRUE
CROSS JOIN (VALUES
    ('builtin_document', '通用文档', '系统内置通用文档模型：标题、Markdown 正文、附件、标签', 'document'),
    ('builtin_note', '通用笔记', '系统内置通用笔记模型：标题、正文、附件、标签', 'note'),
    ('builtin_faq', '常见问题 FAQ', '系统内置 FAQ 模型：以标题为问题、Markdown 正文为答案', 'faq'),
    ('builtin_shot', '经典镜头库', '经典镜头示例专业模型：景别、机位、运镜等结构化字段', 'record')
) AS d(model_key, name, description, content_kind)
ON CONFLICT (organization_id, model_key) DO NOTHING;

INSERT INTO model.resource_model_versions
    (resource_model_id, organization_id, version_no, status, field_schema, form_schema, list_schema,
     policy, schema_checksum, created_by, validated_at, published_at)
SELECT m.id,
       m.organization_id,
       1,
       'published',
       d.field_schema,
       d.form_schema,
       d.list_schema,
       d.policy,
       'seed:v1',
       m.created_by,
       now(),
       now()
FROM model.resource_models m
JOIN (VALUES

  ('builtin_document',
   $schema${"additional_properties": false, "fields": []}$schema$::jsonb,
   $schema${"sections": []}$schema$::jsonb,
   $schema${"columns": ["title", "updated_at"], "filters": []}$schema$::jsonb,
   $policy${
     "visibility": {"default": "workspace", "allowed": ["workspace", "organization", "public"]},
     "channels": {
       "workspace": {"enabled": true},
       "public_site": {"enabled": false},
       "agent": {"enabled": true, "content_scope": "published"},
       "open_api": {"enabled": false, "content_scope": "published"}
     },
     "retrieval": {
       "structured": {"enabled": true},
       "fulltext": {"enabled": true},
       "semantic": {"enabled": true}
     },
     "publishing": {
       "mode": "direct",
       "required_fields": [],
       "require_clean_attachments": true,
       "require_human_confirmation": true
     }
   }$policy$::jsonb),

  ('builtin_note',
   $schema${"additional_properties": false, "fields": []}$schema$::jsonb,
   $schema${"sections": []}$schema$::jsonb,
   $schema${"columns": ["title", "updated_at"], "filters": []}$schema$::jsonb,
   $policy${
     "visibility": {"default": "workspace", "allowed": ["workspace", "organization", "public"]},
     "channels": {
       "workspace": {"enabled": true},
       "public_site": {"enabled": false},
       "agent": {"enabled": true, "content_scope": "published"},
       "open_api": {"enabled": false, "content_scope": "published"}
     },
     "retrieval": {
       "structured": {"enabled": true},
       "fulltext": {"enabled": true},
       "semantic": {"enabled": true}
     },
     "publishing": {
       "mode": "direct",
       "required_fields": [],
       "require_clean_attachments": true,
       "require_human_confirmation": true
     }
   }$policy$::jsonb),

  ('builtin_faq',
   $schema${"additional_properties": false, "fields": []}$schema$::jsonb,
   $schema${"sections": []}$schema$::jsonb,
   $schema${"columns": ["title", "updated_at"], "filters": []}$schema$::jsonb,
   $policy${
     "visibility": {"default": "workspace", "allowed": ["workspace", "organization", "public"]},
     "channels": {
       "workspace": {"enabled": true},
       "public_site": {"enabled": false},
       "agent": {"enabled": true, "content_scope": "published"},
       "open_api": {"enabled": false, "content_scope": "published"}
     },
     "retrieval": {
       "structured": {"enabled": true},
       "fulltext": {"enabled": true},
       "semantic": {"enabled": true}
     },
     "publishing": {
       "mode": "direct",
       "required_fields": [],
       "require_clean_attachments": true,
       "require_human_confirmation": true
     }
   }$policy$::jsonb),

  ('builtin_shot',
   $schema${
     "additional_properties": false,
     "fields": [
       {"key": "shot_size", "type": "enum", "options": [
         {"value": "远景"}, {"value": "全景"}, {"value": "中景"}, {"value": "近景"}, {"value": "特写"}
       ]},
       {"key": "camera_angle", "type": "enum", "options": [
         {"value": "低机位"}, {"value": "平拍"}, {"value": "俯拍"}, {"value": "仰拍"}
       ]},
       {"key": "movement", "type": "enum", "options": [
         {"value": "固定"}, {"value": "推拉"}, {"value": "横摇"}, {"value": "竖摇"}, {"value": "手持"}
       ]},
       {"key": "lens_mm", "type": "integer"},
       {"key": "film_stock", "type": "enum", "options": [
         {"value": "数字"}, {"value": "彩色胶片"}, {"value": "黑白胶片"}
       ]},
       {"key": "location", "type": "string"},
       {"key": "shoot_date", "type": "date"}
     ]
   }$schema$::jsonb,
   $schema${
     "sections": [
       {"title": "画面", "fields": ["shot_size", "camera_angle", "movement"]},
       {"title": "器材与胶片", "fields": ["lens_mm", "film_stock"]},
       {"title": "拍摄信息", "fields": ["location", "shoot_date"]}
     ]
   }$schema$::jsonb,
   $schema${"columns": ["shot_size", "camera_angle", "lens_mm", "location"], "filters": ["shot_size", "camera_angle"]}$schema$::jsonb,
   $policy${
     "visibility": {"default": "workspace", "allowed": ["workspace", "organization", "public"]},
     "channels": {
       "workspace": {"enabled": true},
       "public_site": {"enabled": false},
       "agent": {"enabled": true, "content_scope": "published"},
       "open_api": {"enabled": false, "content_scope": "published"}
     },
     "retrieval": {
       "structured": {"enabled": true},
       "fulltext": {"enabled": true},
       "semantic": {"enabled": true}
     },
     "publishing": {
       "mode": "direct",
       "required_fields": [],
       "require_clean_attachments": true,
       "require_human_confirmation": true
     }
   }$policy$::jsonb)

) AS d(model_key, field_schema, form_schema, list_schema, policy)
  ON d.model_key = m.model_key
WHERE m.model_key IN ('builtin_document', 'builtin_note', 'builtin_faq', 'builtin_shot')
ON CONFLICT (resource_model_id, version_no) DO NOTHING;

UPDATE model.resource_models m
SET current_version_id = v.id,
    updated_at = now()
FROM model.resource_model_versions v
WHERE v.resource_model_id = m.id
  AND v.version_no = 1
  AND v.status = 'published'
  AND m.current_version_id IS NULL
  AND m.model_key IN ('builtin_document', 'builtin_note', 'builtin_faq', 'builtin_shot');

COMMIT;
