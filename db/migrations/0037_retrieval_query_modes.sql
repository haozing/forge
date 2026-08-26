-- Keep the persisted search-session contract in sync with the public query API.
ALTER TABLE retrieval.search_sessions
    DROP CONSTRAINT IF EXISTS search_sessions_mode_check;

ALTER TABLE retrieval.search_sessions
    ADD CONSTRAINT search_sessions_mode_check
    CHECK (mode IN ('structured', 'fulltext', 'semantic', 'hybrid', 'lexical'));

COMMIT;
