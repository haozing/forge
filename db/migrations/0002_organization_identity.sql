-- 0002_organization_identity.sql
-- Organization is the tenant and owner tier. Members authenticate with a
-- globally unique email; agents are technical identities without any member
-- role. Departments, groups and per-organization role tables from v1 are not
-- part of the v2 ownership chain.
BEGIN;

CREATE TABLE organization.organizations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL UNIQUE,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE identity.users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    user_type text NOT NULL CHECK (user_type IN ('member', 'agent')),
    email text,
    display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 80),
    password_hash text,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'deleted')),
    organization_role text CHECK (organization_role IN ('admin', 'member')),
    last_login_at timestamptz,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (user_type <> 'agent' OR organization_role IS NULL),
    CHECK (user_type <> 'member' OR email IS NOT NULL),
    CHECK (user_type <> 'member' OR password_hash IS NOT NULL),
    UNIQUE (email)
);

CREATE INDEX identity_users_org_status_idx
    ON identity.users (organization_id, status, created_at, id);

CREATE TABLE identity.api_keys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES identity.users(id),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    name text NOT NULL,
    key_prefix text NOT NULL,
    key_hash text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked', 'expired')),
    expires_at timestamptz,
    last_used_at timestamptz,
    capabilities jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);

CREATE TABLE identity.sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES identity.users(id),
    token_hash text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    idle_expires_at timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    ip_prefix text,
    user_agent text CHECK (user_agent IS NULL OR char_length(user_agent) <= 300)
);

CREATE INDEX identity_sessions_active_idx
    ON identity.sessions (user_id, revoked_at, absolute_expires_at);

CREATE TABLE identity.password_resets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES identity.users(id),
    token_hash text NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX identity_password_resets_token_idx
    ON identity.password_resets (token_hash);

CREATE TABLE identity.user_preferences (
    user_id uuid PRIMARY KEY REFERENCES identity.users(id) ON DELETE CASCADE,
    default_workspace_id uuid,
    timezone text NOT NULL DEFAULT 'UTC',
    email_notifications_enabled boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE security.auth_rate_limits (
    bucket_type text NOT NULL CHECK (bucket_type IN
        ('login_email', 'login_ip', 'password_reset_email', 'password_reset_ip', 'invitation_ip')),
    key_hash text NOT NULL,
    window_started_at timestamptz NOT NULL DEFAULT now(),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    blocked_until timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (bucket_type, key_hash)
);

CREATE TABLE notification.email_deliveries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    template text NOT NULL CHECK (template IN ('organization_invitation', 'password_reset')),
    recipient_email text NOT NULL,
    key_version integer NOT NULL CHECK (key_version > 0),
    encrypted_payload bytea NOT NULL,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'cancelled')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    locked_by text,
    locked_until timestamptz,
    sent_at timestamptz,
    provider_message_id text,
    last_error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX notification_email_deliveries_due_idx
    ON notification.email_deliveries (status, next_attempt_at, id);

CREATE TABLE "authorization".policy_revisions (
    organization_id uuid PRIMARY KEY REFERENCES organization.organizations(id),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

COMMIT;
