-- 0018: derivation relations unify into asset.asset_relations.
--
-- Phase decision (2026-09-03, product doc v2 §5.2/§9 and the "trunk +
-- derivation" input-page spec): a derivation is harvest-then-copy. The
-- lineage edge is an ordinary AssetRelation out-edge of the derived version
-- (derived_from / continues_from, source='manual', derivation id in the
-- citation payload), materialized inside the version's creating transaction
-- under the sealed-endpoint guard, and visible to
-- GET /api/assets/{assetId}/relations. content.asset_relations (and its
-- content.asset_relation_blocks detail table) duplicated that fact in a
-- shape no reader consumed; both are dropped.

DROP TABLE IF EXISTS content.asset_relation_blocks;
DROP TABLE IF EXISTS content.asset_relations;
