-- 0014_agent_answer_posture.sql
-- The conversation surface serves two postures: co_create thoughts (the
-- human-machine co-creation entry - the model may reason freely, retrieved
-- content is reference material, no citations) and grounded_qa retrieval
-- answers (published assets are the only basis, citations mandatory,
-- insufficient knowledge is stated plainly). The application column carries
-- the default; requests may still override through response_mode.

ALTER TABLE integration.agent_applications
    ADD COLUMN answer_posture text NOT NULL DEFAULT 'co_create'
        CHECK (answer_posture IN ('co_create', 'grounded_qa'));
