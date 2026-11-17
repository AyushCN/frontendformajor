-- 0006_projects_collaboration.up.sql — Projects, Memberships, Invitations, Repositories, Sandboxes & Audit Events

-- 1. Projects
CREATE TABLE projects (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    is_default  BOOLEAN NOT NULL DEFAULT false,
    created_by  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_projects_created_by ON projects(created_by);
CREATE INDEX idx_projects_is_default ON projects(is_default);

-- 2. Project Repositories
CREATE TABLE project_repositories (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id     UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    git_url        TEXT NOT NULL,
    default_branch TEXT NOT NULL DEFAULT 'main',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, git_url)
);
CREATE INDEX idx_project_repositories_project ON project_repositories(project_id);

-- 3. Project Members
CREATE TABLE project_members (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role            TEXT NOT NULL,  -- OWNER | EDITOR | VIEWER
    status          TEXT NOT NULL DEFAULT 'ACTIVE', -- ACTIVE | PENDING
    invited_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    invited_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    accepted_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, user_id)
);
CREATE INDEX idx_project_members_project ON project_members(project_id);
CREATE INDEX idx_project_members_user ON project_members(user_id);
CREATE INDEX idx_project_members_role ON project_members(role);

-- 4. Project Invitations
CREATE TABLE project_invitations (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    inviter_id  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invitee_id  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role        TEXT NOT NULL,  -- EDITOR | VIEWER
    status      TEXT NOT NULL DEFAULT 'PENDING', -- PENDING | ACCEPTED | REJECTED | REVOKED | EXPIRED
    expires_at  TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_project_invitations_project ON project_invitations(project_id);
CREATE INDEX idx_project_invitations_invitee ON project_invitations(invitee_id);
CREATE INDEX idx_project_invitations_status ON project_invitations(status);

-- 5. Project Sandboxes (associations between projects and sandboxes)
CREATE TABLE project_sandboxes (
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    sandbox_id  UUID NOT NULL REFERENCES sandboxes(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, sandbox_id)
);
CREATE INDEX idx_project_sandboxes_project ON project_sandboxes(project_id);
CREATE INDEX idx_project_sandboxes_sandbox ON project_sandboxes(sandbox_id);

-- 6. Audit Events
CREATE TABLE audit_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID REFERENCES projects(id) ON DELETE CASCADE,
    user_id     UUID REFERENCES users(id) ON DELETE SET NULL,
    action      TEXT NOT NULL,
    resource    TEXT NOT NULL,
    details     JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_events_project ON audit_events(project_id);
CREATE INDEX idx_audit_events_created_at ON audit_events(created_at);
