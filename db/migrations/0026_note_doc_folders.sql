-- 0026_note_doc_folders.sql — 笔记/知识库目录树容器。
-- content.containers 的 kind 枚举扩展出两种纯组织性容器：note_folder
-- （"我的笔记"多级目录，承载会话绑定的灵感笔记）与 doc_folder（知识库
-- 树形分类，承载已发布文档）。两者都是无 asset_id 的目录节点，父子
-- 关系走既有的 parent_id，同层排序走 sort_key；资产挂载沿用
-- content.container_assets 关系表。

ALTER TABLE content.containers DROP CONSTRAINT containers_kind_check;

ALTER TABLE content.containers ADD CONSTRAINT containers_kind_check
    CHECK (kind IN ('chat', 'note', 'document', 'faq', 'content_field', 'custom',
                    'note_folder', 'doc_folder'));
