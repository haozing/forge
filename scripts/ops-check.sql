SELECT json_build_object(
  'dead_deliveries', (SELECT count(*) FROM audit.event_deliveries WHERE status = 'dead'),
  'retrying_deliveries', (SELECT count(*) FROM audit.event_deliveries WHERE status = 'retry_wait'),
  'failed_processing_jobs', (SELECT count(*) FROM content.processing_jobs WHERE status = 'failed'),
  'stuck_processing_jobs', (SELECT count(*) FROM content.processing_jobs WHERE status = 'running' AND started_at < now() - interval '15 minutes')
);
