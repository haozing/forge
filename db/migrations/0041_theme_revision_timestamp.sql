-- 0041: site_theme_revisions 补 updated_at（草稿保存更新时间戳）。
ALTER TABLE site.site_theme_revisions
    ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();
