-- 0008_runtime_telemetry.down.sql

ALTER TABLE deployments
    DROP COLUMN IF EXISTS requested_runtime,
    DROP COLUMN IF EXISTS resolved_runtime,
    DROP COLUMN IF EXISTS runtime_artifact_id,
    DROP COLUMN IF EXISTS provisioning_status,
    DROP COLUMN IF EXISTS build_failure_reason;
