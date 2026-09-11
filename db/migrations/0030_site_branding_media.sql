-- 0030: 站点品牌媒体（Logo / Favicon / 社交分享图）。
-- 产品文档：CMS 呈现层设计方案 §10.7（外观 Token 含 Logo、Favicon 和分享图）。
-- 引用 open/会员面上传的 asset.attachments（image/*），由交付面的媒体路由
-- 校验后对外服务；站点 PATCH 可整体更新，Release 快照随站点行走。
ALTER TABLE site.public_sites
    ADD COLUMN logo_attachment_id uuid,
    ADD COLUMN favicon_attachment_id uuid,
    ADD COLUMN social_image_attachment_id uuid;

ALTER TABLE site.public_sites
    ADD CONSTRAINT site_logo_attachment_fk
        FOREIGN KEY (logo_attachment_id) REFERENCES asset.attachments(id),
    ADD CONSTRAINT site_favicon_attachment_fk
        FOREIGN KEY (favicon_attachment_id) REFERENCES asset.attachments(id),
    ADD CONSTRAINT site_social_image_attachment_fk
        FOREIGN KEY (social_image_attachment_id) REFERENCES asset.attachments(id);
