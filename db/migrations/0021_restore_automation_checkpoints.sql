-- 0021_restore_automation_checkpoints.sql
-- Restores the automation.checkpoints table that the phase0 baseline rewrite
-- dropped (43333c5) while the Go checkpoint store kept running against it:
-- internal/agentruntime/checkpoint writes Eino graph state per run, and
-- automation.runs still carries eino_checkpoint_id / checkpoint_sequence.
-- Any react run crossing a process boundary failed on the missing relation.
-- DDL is byte-equivalent to the pre-phase0 definition.

CREATE TABLE automation.checkpoints (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    run_id uuid NOT NULL REFERENCES automation.runs(id) ON DELETE CASCADE,
    sequence bigint NOT NULL CHECK (sequence > 0),
    checkpoint_key text NOT NULL,
    payload_ciphertext bytea NOT NULL,
    payload_checksum text NOT NULL,
    graph_code_version bigint,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, sequence)
);

CREATE INDEX automation_checkpoints_run_idx
    ON automation.checkpoints (organization_id, run_id, sequence DESC);
