-- Phase 4: add development-preview mode columns to sandboxes.
-- sandbox_mode: 'deployment' (default, full image rebuild on save)
--               'development' (bind-mount workspace, language-aware hot reload)
-- dev_container_id: Docker container ID of the running dev container
--                   (NULL when not in dev mode or dev container not started).
-- dev_container_port: port the dev container's app listens on
--                     (NULL when not in dev mode).
ALTER TABLE sandboxes
  ADD COLUMN IF NOT EXISTS sandbox_mode       TEXT NOT NULL DEFAULT 'deployment',
  ADD COLUMN IF NOT EXISTS dev_container_id   TEXT,
  ADD COLUMN IF NOT EXISTS dev_container_port INT;
