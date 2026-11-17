-- 0005_resettable_hibernated_expiration.up.sql
--
-- Adds resettable sliding expiration based on hibernation lifecycle:
-- - hibernated_at: records when the sandbox entered HIBERNATED
-- - hibernated_expiration_seconds: sliding TTL after hibernation (default: 2 hours = 7200s)
-- - workspace_retention_seconds: cold data retention duration after expiration (default: 7 days = 604800s)
-- - lifecycle_version: monotonic fencing counter to invalidate stale expiration jobs

ALTER TABLE sandboxes
    ADD COLUMN IF NOT EXISTS hibernated_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS hibernated_expiration_seconds BIGINT NOT NULL DEFAULT 7200,
    ADD COLUMN IF NOT EXISTS workspace_retention_seconds BIGINT NOT NULL DEFAULT 604800,
    ADD COLUMN IF NOT EXISTS lifecycle_version BIGINT NOT NULL DEFAULT 1;

COMMENT ON COLUMN sandboxes.hibernated_at IS 'Timestamp when sandbox entered HIBERNATED; NULL while RUNNING';
COMMENT ON COLUMN sandboxes.hibernated_expiration_seconds IS 'Sliding expiration deadline after entering hibernation in seconds (default 2h)';
COMMENT ON COLUMN sandboxes.workspace_retention_seconds IS 'Retention duration for on-disk workspace data after container expiration in seconds (default 7d)';
COMMENT ON COLUMN sandboxes.lifecycle_version IS 'Monotonic fencing token incremented on every state transition to cancel stale jobs';

