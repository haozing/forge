-- 0042_site_description.sql — 站点级描述（§1.3/§7.3）：agent 与运营可改，
-- 作为公开首页 meta description 的兜底来源（此前恒为站点名）。
ALTER TABLE site.public_sites ADD COLUMN IF NOT EXISTS description text NOT NULL DEFAULT '';
