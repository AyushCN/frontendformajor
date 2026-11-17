ALTER TABLE sandboxes
    DROP COLUMN IF EXISTS hibernated_at,
    DROP COLUMN IF EXISTS hibernated_expiration_seconds,
    DROP COLUMN IF EXISTS workspace_retention_seconds,
    DROP COLUMN IF EXISTS lifecycle_version;

