-- 0012_sandbox_project_association_integrity.up.sql
-- Enforce 1-to-1 authoritative relationship between sandbox and project

-- 1. Reconcile duplicate associations by keeping the newest record per sandbox_id
DELETE FROM project_sandboxes a
USING project_sandboxes b
WHERE a.sandbox_id = b.sandbox_id
  AND (a.created_at < b.created_at OR (a.created_at = b.created_at AND a.project_id < b.project_id));

-- 2. Remove any orphaned project_sandboxes referencing nonexistent projects or sandboxes
DELETE FROM project_sandboxes WHERE project_id NOT IN (SELECT id FROM projects);
DELETE FROM project_sandboxes WHERE sandbox_id NOT IN (SELECT id FROM sandboxes);

-- 3. Enforce uniqueness: exactly one project association per sandbox
ALTER TABLE project_sandboxes
    ADD CONSTRAINT uniq_project_sandboxes_sandbox_id UNIQUE (sandbox_id);
