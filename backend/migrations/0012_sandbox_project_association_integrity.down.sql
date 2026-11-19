-- 0012_sandbox_project_association_integrity.down.sql
ALTER TABLE project_sandboxes
    DROP CONSTRAINT IF EXISTS uniq_project_sandboxes_sandbox_id;
