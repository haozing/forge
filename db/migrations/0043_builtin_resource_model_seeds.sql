BEGIN;

-- 0043_builtin_resource_model_seeds
-- Seeds the builtin resource models required by docs/产品文档.md (§1.5/§6.2/P0-2/#22):
--   builtin_document / builtin_note / builtin_faq (fixed system fields only) and
--   builtin_shot (classic-shot example professional model with a CMS form).
-- For every organization: one active model + one published initial version,
-- then resource_models.current_version_id is backfilled so the models enter
-- the outlets validation chain immediately.
-- Idempotent: re-running is safe; existing rows are left untouched.

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
    -- The org admin created by cmd/bootstrap owns the seeded models.
    SELECT u.id
    FROM identity.users u
    WHERE u.organization_id = o.id
      AND u.member_role = 'admin'
      AND u.user_type = 'member'
      AND u.status = 'active'
    ORDER BY u.created_at, u.id
    LIMIT 1
) a ON TRUE
CROSS JOIN (VALUES
    ('builtin_document',	'通用文档',	'系统内置通用文档模型：标题、Markdown 正文、附件、标签（产品文档 §6.2）',	'document'),
    ('builtin_note',	'通用笔记',	'系统内置通用笔记模型：默认笔记文档载体，标题、正文、附件、标签（产品文档 §6.2.1）',	'note'),
    ('builtin_faq',	'常见问题 FAQ',	'系统内置 FAQ 模型：以标题为问题、Markdown 正文为答案，便于检索引用',	'faq'),
    ('builtin_shot',	'经典镜头库',	'经典镜头示例专业模型：CMS 动态表单演示景别、机位、运镜等结构化字段（产品文档 §1.5/§6.2）',	'record')

) AS d(model_key, name, description, content_kind)
ON CONFLICT (organization_id, model_key) DO NOTHING;

-- workspace_id stays NULL on purpose: builtin models are org-wide and the
-- column is nullable, so no per-workspace binding is required.
INSERT INTO model.resource_model_versions
    (resource_model_id, version_no, status, field_schema, form_schema, list_schema,
     policy, schema_checksum, created_by, validated_at, published_at)
SELECT m.id,
       1,
       'published',
       d.field_schema,
       d.form_schema,
       d.list_schema,
       d.policy,
       d.schema_checksum,
       m.created_by,
       now(),
       now()
FROM model.resource_models m
JOIN (VALUES
  ('builtin_document', $schema${
  "additional_properties": false,
  "fields": []
}$schema$::jsonb,
   $schema${
  "sections": []
}$schema$::jsonb,
   $schema${
  "columns": [
    "title",
    "updated_at"
  ],
  "filters": [
    "tags"
  ]
}$schema$::jsonb,
   $schema${
  "outlets": {
    "agent_tool": {
      "enabled": true
    },
    "fulltext": {
      "enabled": true
    },
    "semantic": {
      "enabled": true
    },
    "workspace": {
      "enabled": true
    }
  },
  "visibility": {
    "allowed": [
      "public",
      "login",
      "private",
      "workspace",
      "internal"
    ],
    "default": "workspace"
  }
}$schema$::jsonb,
   '0b5add4f26fb4afcd597dead198df0ad4291e5908f1867d1ac2ff42d1ba4e016'),
  ('builtin_note', $schema${
  "additional_properties": false,
  "fields": []
}$schema$::jsonb,
   $schema${
  "sections": []
}$schema$::jsonb,
   $schema${
  "columns": [
    "title",
    "updated_at"
  ],
  "filters": [
    "tags"
  ]
}$schema$::jsonb,
   $schema${
  "outlets": {
    "agent_tool": {
      "enabled": true
    },
    "fulltext": {
      "enabled": true
    },
    "semantic": {
      "enabled": true
    },
    "workspace": {
      "enabled": true
    }
  },
  "visibility": {
    "allowed": [
      "public",
      "login",
      "private",
      "workspace",
      "internal"
    ],
    "default": "workspace"
  }
}$schema$::jsonb,
   '61ab873c8d3219ba3e5e73e98c3bd0aafb31bffc1878c9a1618da4f752e80ca6'),
  ('builtin_faq', $schema${
  "additional_properties": false,
  "fields": []
}$schema$::jsonb,
   $schema${
  "sections": []
}$schema$::jsonb,
   $schema${
  "columns": [
    "title",
    "updated_at"
  ],
  "filters": [
    "tags"
  ]
}$schema$::jsonb,
   $schema${
  "outlets": {
    "agent_tool": {
      "enabled": true
    },
    "fulltext": {
      "enabled": true
    },
    "semantic": {
      "enabled": true
    },
    "workspace": {
      "enabled": true
    }
  },
  "visibility": {
    "allowed": [
      "public",
      "login",
      "private",
      "workspace",
      "internal"
    ],
    "default": "workspace"
  }
}$schema$::jsonb,
   'ec86f1f287956a038b4218a7f59181947e6d9867d73110c9d3d42daa643e0765'),
  ('builtin_shot', $schema${
  "additional_properties": false,
  "fields": [
    {
      "key": "shot_size",
      "options": [
        {
          "value": "远景"
        },
        {
          "value": "全景"
        },
        {
          "value": "中景"
        },
        {
          "value": "近景"
        },
        {
          "value": "特写"
        }
      ],
      "type": "enum"
    },
    {
      "key": "camera_angle",
      "options": [
        {
          "value": "低机位"
        },
        {
          "value": "平拍"
        },
        {
          "value": "俯拍"
        },
        {
          "value": "仰拍"
        }
      ],
      "type": "enum"
    },
    {
      "key": "movement",
      "options": [
        {
          "value": "固定"
        },
        {
          "value": "推拉"
        },
        {
          "value": "横摇"
        },
        {
          "value": "竖摇"
        },
        {
          "value": "手持"
        }
      ],
      "type": "enum"
    },
    {
      "key": "lens_mm",
      "type": "integer"
    },
    {
      "key": "film_stock",
      "options": [
        {
          "value": "数字"
        },
        {
          "value": "彩色胶片"
        },
        {
          "value": "黑白胶片"
        }
      ],
      "type": "enum"
    },
    {
      "key": "location",
      "type": "string"
    },
    {
      "key": "shoot_date",
      "type": "date"
    }
  ]
}$schema$::jsonb,
   $schema${
  "sections": [
    {
      "fields": [
        "shot_size",
        "camera_angle",
        "movement"
      ],
      "title": "画面"
    },
    {
      "fields": [
        "lens_mm",
        "film_stock"
      ],
      "title": "器材与胶片"
    },
    {
      "fields": [
        "location",
        "shoot_date"
      ],
      "title": "拍摄信息"
    }
  ]
}$schema$::jsonb,
   $schema${
  "columns": [
    "shot_size",
    "camera_angle",
    "lens_mm",
    "location"
  ],
  "filters": [
    "shot_size",
    "camera_angle"
  ]
}$schema$::jsonb,
   $schema${
  "outlets": {
    "agent_tool": {
      "enabled": true
    },
    "fulltext": {
      "enabled": true
    },
    "semantic": {
      "enabled": true
    },
    "workspace": {
      "enabled": true
    }
  },
  "visibility": {
    "allowed": [
      "public",
      "login",
      "private",
      "workspace",
      "internal"
    ],
    "default": "workspace"
  }
}$schema$::jsonb,
   '33246aa9b009ec65b940a172f276ad469d84fbcb0a74f7fa68664e67929bc2d2')

) AS d(model_key, field_schema, form_schema, list_schema, policy, schema_checksum)
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
