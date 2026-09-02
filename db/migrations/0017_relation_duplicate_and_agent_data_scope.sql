-- 0017_relation_duplicate_and_agent_data_scope.sql
-- Appends to the v2 baseline (append-only since the stage 6 exit):
-- 1. the duplicate relation type joins the relation vocabulary (product doc
--    §4 AssetRelation: 来源、引用、派生、重复、相关). asset.asset_relations
--    itself has no relation_type CHECK — the vocabulary lives in Go
--    (asset.ValidRelationType); the two staging tables carry DB CHECKs and
--    must widen with it;
-- 2. AgentAccessPolicy gains the per-policy data scope from doc §10.4 —
--    public (public band only), organization (public + organization bands,
--    the previous fixed behavior, hence the default) or workspace (the full
--    internal band). No backfill: the default preserves every existing row.

ALTER TABLE asset.asset_draft_relations
    DROP CONSTRAINT asset_draft_relations_relation_type_check;
ALTER TABLE asset.asset_draft_relations
    ADD CONSTRAINT asset_draft_relations_relation_type_check
    CHECK (relation_type IN ('related_to', 'references', 'derived_from', 'cites', 'continues_from', 'duplicate'));

ALTER TABLE asset.asset_relation_suggestions
    DROP CONSTRAINT asset_relation_suggestions_relation_type_check;
ALTER TABLE asset.asset_relation_suggestions
    ADD CONSTRAINT asset_relation_suggestions_relation_type_check
    CHECK (relation_type IN ('related_to', 'references', 'derived_from', 'cites', 'continues_from', 'duplicate'));

ALTER TABLE content.agent_access_policies
    ADD COLUMN data_scope text NOT NULL DEFAULT 'organization'
        CHECK (data_scope IN ('public', 'organization', 'workspace'));
