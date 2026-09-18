-- 0043: 站点简报（内容治理四件套 F1，对应 llm_wiki purpose.md）。
-- "为什么"是散文不是表单：单列 markdown，仅注入 react run 指令与内部
-- 编辑面，不进公开渲染（公开 meta 兜底链仍走 description）。
ALTER TABLE site.public_sites ADD COLUMN IF NOT EXISTS brief text NOT NULL DEFAULT '';
