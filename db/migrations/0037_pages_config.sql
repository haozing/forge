-- 0037: 页面配置 v2（站点方案 C1，2026-09-12）。
-- pages_config 承载：home blocks（七型模块目录）、自定义页（/p/{slug}，
-- 上限 20）、集合页参数、导航（自动枚举 + 隐藏/排序/外链）。
-- 空文档 = 渲染层回退默认首页（hero + 最新内容），零配置即可用。

ALTER TABLE site.public_sites
    ADD COLUMN pages_config jsonb NOT NULL DEFAULT '{}'::jsonb;
