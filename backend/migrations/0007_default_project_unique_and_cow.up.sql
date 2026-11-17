-- 0007_default_project_unique_and_cow.up.sql
-- Enforce database-level uniqueness: exactly one default project per user
CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_user_default
ON projects (created_by)
WHERE is_default = TRUE;

-- Add index on sandboxes parent_sandbox_id for fast CoW lineage lookups
CREATE INDEX IF NOT EXISTS idx_sandboxes_parent_sandbox_id
ON sandboxes (parent_sandbox_id);
