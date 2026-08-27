-- One-off hygiene: history allowed chunks for draft/archived asset versions to
-- linger in the retrieval index. Retract every chunk whose version is not the
-- owning asset's current published version, so recall can only see published
-- content going forward (guards now enforced in code as well).
UPDATE retrieval.chunks c
SET status = 'deleted', search_text = '', content = '', updated_at = now()
FROM asset.asset_versions av
JOIN asset.assets a ON a.id = av.asset_id
WHERE c.asset_version_id = av.id
  AND c.status <> 'deleted'
  AND (a.current_published_version_id IS DISTINCT FROM av.id OR a.publication_status <> 'published');

UPDATE retrieval.chunk_embeddings e
SET status = 'deleted', updated_at = now()
FROM retrieval.chunks c
WHERE e.chunk_id = c.id
  AND e.status <> 'deleted'
  AND c.status = 'deleted';

UPDATE retrieval.projection_runs pr
SET status = 'stale', updated_at = now()
FROM asset.asset_versions av
JOIN asset.assets a ON a.id = av.asset_id
WHERE pr.asset_version_id = av.id
  AND pr.status NOT IN ('stale')
  AND (a.current_published_version_id IS DISTINCT FROM av.id OR a.publication_status <> 'published');
