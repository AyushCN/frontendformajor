ALTER TABLE sandbox_env_vars
  DROP COLUMN IF EXISTS created_at,
  DROP COLUMN IF EXISTS updated_at;

ALTER TABLE project_members
  DROP COLUMN IF EXISTS can_manage_secrets;
