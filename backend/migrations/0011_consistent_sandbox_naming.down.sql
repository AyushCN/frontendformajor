-- 0011_consistent_sandbox_naming.down.sql
DROP TABLE IF EXISTS sandbox_subdomain_aliases;
ALTER TABLE sandboxes DROP COLUMN IF EXISTS slug;
ALTER TABLE sandboxes DROP COLUMN IF EXISTS name;
