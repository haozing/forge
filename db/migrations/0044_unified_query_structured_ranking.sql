-- Allow the member unified-query session tables to record the new
-- 'structured' ranking method introduced with doc-aligned query modes.
ALTER TABLE retrieval.search_sessions
    DROP CONSTRAINT search_sessions_ranking_method_check;
ALTER TABLE retrieval.search_sessions
    ADD CONSTRAINT search_sessions_ranking_method_check
    CHECK (ranking_method = ANY (ARRAY['lexical'::text, 'vector'::text, 'rrf'::text, 'rerank'::text, 'structured'::text]));

ALTER TABLE retrieval.search_session_items
    DROP CONSTRAINT search_session_items_ranking_method_check;
ALTER TABLE retrieval.search_session_items
    ADD CONSTRAINT search_session_items_ranking_method_check
    CHECK (ranking_method = ANY (ARRAY['lexical'::text, 'vector'::text, 'rrf'::text, 'rerank'::text, 'structured'::text]));
