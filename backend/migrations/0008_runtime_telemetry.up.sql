-- 0008_runtime_telemetry.up.sql
--
-- Adds runtime availability, provisioning, and caching observability to deployments.

ALTER TABLE deployments
    ADD COLUMN IF NOT EXISTS requested_runtime TEXT,
    ADD COLUMN IF NOT EXISTS resolved_runtime TEXT,
    ADD COLUMN IF NOT EXISTS runtime_artifact_id TEXT,
    ADD COLUMN IF NOT EXISTS provisioning_status TEXT,
    ADD COLUMN IF NOT EXISTS build_failure_reason TEXT;

COMMENT ON COLUMN deployments.requested_runtime IS 'Requested runtime language and version requirement';
COMMENT ON COLUMN deployments.resolved_runtime IS 'Resolved exact runtime language and version';
COMMENT ON COLUMN deployments.runtime_artifact_id IS 'Immutable runtime artifact identity in shared cache';
COMMENT ON COLUMN deployments.provisioning_status IS 'Provisioning outcome (e.g. VERSION_SUPPORTED, VERSION_PROVISIONING_REQUIRED)';
COMMENT ON COLUMN deployments.build_failure_reason IS 'Categorized build or runtime provisioning failure reason';
