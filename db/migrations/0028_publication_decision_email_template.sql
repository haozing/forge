-- 0028_publication_decision_email_template.sql
-- N8 审核决定邮件入队使用 template='publication_decision'（review 域
-- enqueueDecisionEmailTx），但 email_deliveries 的 CHECK 仍只允许邀请与
-- 密码重置两个模板——不扩枚举，任何配置了 PUBLIC_APP_BASE_URL 的环境的
-- 审批/驳回都会在入队时违反约束并回滚整个决策事务。与
-- internal/notification/templates.go 的 TemplatePublicationDecision 对齐。

ALTER TABLE notification.email_deliveries DROP CONSTRAINT email_deliveries_template_check;

ALTER TABLE notification.email_deliveries ADD CONSTRAINT email_deliveries_template_check
    CHECK (template IN ('organization_invitation', 'password_reset', 'publication_decision'));
