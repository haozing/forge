-- 0038: 多语言站点配置（站点方案 D11/D12，2026-09-13）。
-- default_locale：新建内容的默认语言（zh）；enabled_locales：对外提供的
-- 语言集合（仅默认语言 = 整条多语言链路不激活，行为与今天一致）；
-- fallback_to_default：列表回退开关（当前语言无内容时显示默认语言版本）。

ALTER TABLE site.public_sites
    ADD COLUMN default_locale text NOT NULL DEFAULT 'zh',
    ADD COLUMN enabled_locales text[] NOT NULL DEFAULT '{zh}',
    ADD COLUMN fallback_to_default boolean NOT NULL DEFAULT true;
