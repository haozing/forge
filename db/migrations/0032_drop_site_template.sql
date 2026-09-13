-- 0032: 删除站点模板死字段（站点方案 D15）。
-- template（blog/pro）自 0010 起从未被任何模板消费，唯一实际用途是首页
-- meta description 拼接（已随本次改造移除）。外观由 style_config +
-- layout 开关决定；"预设"语义由 site.style_presets 承担。
ALTER TABLE site.public_sites DROP COLUMN template;
