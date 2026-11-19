ALTER TABLE sandboxes
  DROP COLUMN IF EXISTS sandbox_mode,
  DROP COLUMN IF EXISTS dev_container_id,
  DROP COLUMN IF EXISTS dev_container_port;
