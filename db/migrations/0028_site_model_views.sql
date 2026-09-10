-- 0028_site_model_views.sql
-- Site-level per-model field display whitelist (CMS plan §7.2 model_views):
-- which structured fields of each resource model a site publishes on cards
-- and detail pages. '{}' = zero fields anywhere (fail-closed default).
-- This also TIGHTENS the public detail JSON: pre-2026-09-10 it returned every
-- schema-declared field; now only whitelisted keys leave the read side.
ALTER TABLE site.public_sites
    ADD COLUMN model_views jsonb NOT NULL DEFAULT '{}';
