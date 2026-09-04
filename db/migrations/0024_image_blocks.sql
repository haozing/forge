-- 0024_image_blocks.sql
-- 正文图片通道（2026-09-04 方案 §2.1）：块词表新增 image 块类型——块存
-- attachment 引用，文件本体留在对象存储；附件表增加识图产出的默认 alt 列。
-- 零数据迁移：开发期免兼容，存量数据不含 image 块，heading 无 level 的
-- 块按级别 3 解释（与既有渲染行为一致）。

ALTER TABLE content.blocks DROP CONSTRAINT blocks_block_type_check;
ALTER TABLE content.blocks ADD CONSTRAINT blocks_block_type_check CHECK (block_type IN (
    'text', 'paragraph', 'heading', 'list', 'code', 'quote', 'qa',
    'question', 'answer', 'message', 'link', 'attachment', 'callout', 'image'
));

ALTER TABLE asset.attachments ADD COLUMN default_alt_text text NOT NULL DEFAULT '';
