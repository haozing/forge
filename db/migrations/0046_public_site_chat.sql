-- 0046 公开站 AI 问答（/ask）：成员登录 + 每日额度的站点问答。
-- 会话与消息（额度=当日 role='user' 消息计数，不建额度表）；
-- 站点列 chat_agent_application_id 指向品牌问答绑定的 agent 应用
--（模型经 RAGRuntime.ModelResolver 按应用解析）。
CREATE TABLE site.chat_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    site_id uuid NOT NULL REFERENCES site.public_sites(id),
    member_id uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, member_id)
);

CREATE TABLE site.chat_messages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id uuid NOT NULL REFERENCES site.chat_sessions(id) ON DELETE CASCADE,
    organization_id uuid NOT NULL,
    site_id uuid NOT NULL,
    member_id uuid NOT NULL,
    role text NOT NULL CHECK (role IN ('user', 'assistant')),
    content text NOT NULL,
    citations jsonb NOT NULL DEFAULT '[]'::jsonb,
    tokens int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_chat_messages_session ON site.chat_messages (session_id, created_at);
CREATE INDEX idx_chat_messages_quota ON site.chat_messages (member_id, site_id, created_at)
    WHERE role = 'user';

ALTER TABLE site.public_sites
    ADD COLUMN chat_agent_application_id uuid;
