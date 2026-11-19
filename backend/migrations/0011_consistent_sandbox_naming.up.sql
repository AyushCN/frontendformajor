-- 0011_consistent_sandbox_naming.up.sql
-- Add user-defined display name and normalized slug to sandboxes.
-- Support alias routing for renamed sandboxes.

ALTER TABLE sandboxes ADD COLUMN IF NOT EXISTS name text NOT NULL DEFAULT '';
ALTER TABLE sandboxes ADD COLUMN IF NOT EXISTS slug text NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS sandbox_subdomain_aliases (
    id uuid PRIMARY KEY,
    sandbox_id uuid NOT NULL REFERENCES sandboxes(id) ON DELETE CASCADE,
    subdomain text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sandbox_subdomain_aliases_sandbox ON sandbox_subdomain_aliases(sandbox_id);
CREATE INDEX IF NOT EXISTS idx_sandbox_subdomain_aliases_subdomain ON sandbox_subdomain_aliases(subdomain);

-- Backfill name and slug for existing rows
UPDATE sandboxes SET slug = SPLIT_PART(subdomain, '-', 1) WHERE slug = '' AND subdomain != '';
UPDATE sandboxes SET name = slug WHERE name = '' AND slug != '';
UPDATE sandboxes SET name = 'sandbox-' || SUBSTRING(id::text, 1, 8) WHERE name = '';
UPDATE sandboxes SET slug = 'sandbox-' || SUBSTRING(id::text, 1, 8) WHERE slug = '';
