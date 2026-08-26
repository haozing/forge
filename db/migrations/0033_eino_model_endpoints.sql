BEGIN;

CREATE TABLE integration.model_endpoints (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    current_revision bigint NOT NULL DEFAULT 1 CHECK (current_revision > 0),
    status text NOT NULL DEFAULT 'unavailable'
        CHECK (status IN ('active', 'disabled', 'unavailable')),
    last_verified_at timestamptz,
    last_health_error_code text,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name),
    UNIQUE (organization_id, id)
);

CREATE TABLE integration.model_endpoint_revisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    model_endpoint_id uuid NOT NULL REFERENCES integration.model_endpoints(id) ON DELETE CASCADE,
    revision bigint NOT NULL CHECK (revision > 0),
    provider_type text NOT NULL CHECK (provider_type IN ('openai', 'openai_compatible')),
    base_url text NOT NULL CHECK (length(btrim(base_url)) BETWEEN 1 AND 2048),
    model_name text NOT NULL CHECK (length(btrim(model_name)) BETWEEN 1 AND 200),
    credential_mode text NOT NULL CHECK (credential_mode IN ('encrypted', 'secret_ref')),
    credential_ciphertext bytea,
    credential_key_id text,
    secret_ref text,
    options jsonb NOT NULL DEFAULT '{}'::jsonb,
    capabilities jsonb NOT NULL DEFAULT '{}'::jsonb,
    config_checksum text NOT NULL,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    UNIQUE (model_endpoint_id, revision),
    CHECK (
        (credential_mode = 'encrypted' AND credential_ciphertext IS NOT NULL AND secret_ref IS NULL)
        OR
        (credential_mode = 'secret_ref' AND credential_ciphertext IS NULL AND length(btrim(secret_ref)) > 0)
    )
);

ALTER TABLE integration.model_endpoints
    ADD CONSTRAINT model_endpoints_current_revision_fk
    FOREIGN KEY (id, current_revision)
    REFERENCES integration.model_endpoint_revisions(model_endpoint_id, revision)
    DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE integration.agent_applications
    ADD COLUMN model_endpoint_id uuid,
    ADD COLUMN runtime_mode text NOT NULL DEFAULT 'rag'
        CHECK (runtime_mode IN ('rag', 'react', 'workflow')),
    ADD COLUMN workflow_key text,
    ADD COLUMN instruction_version bigint NOT NULL DEFAULT 1 CHECK (instruction_version > 0),
    ADD COLUMN tool_policy jsonb NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE integration.agent_applications
    ADD CONSTRAINT agent_applications_model_endpoint_org_fk
    FOREIGN KEY (organization_id, model_endpoint_id)
    REFERENCES integration.model_endpoints(organization_id, id);

CREATE INDEX model_endpoints_org_status_idx
    ON integration.model_endpoints (organization_id, status, updated_at DESC);

CREATE INDEX model_endpoint_revisions_endpoint_idx
    ON integration.model_endpoint_revisions (model_endpoint_id, revision DESC);

CREATE INDEX agent_applications_model_endpoint_idx
    ON integration.agent_applications (organization_id, model_endpoint_id)
    WHERE model_endpoint_id IS NOT NULL;

COMMIT;
