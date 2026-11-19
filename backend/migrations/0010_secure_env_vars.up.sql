-- Migration 0010: Secure environment variables metadata and secrets manager permission
ALTER TABLE sandbox_env_vars
  ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

ALTER TABLE project_members
  ADD COLUMN IF NOT EXISTS can_manage_secrets BOOLEAN NOT NULL DEFAULT false;
