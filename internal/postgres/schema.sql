CREATE TABLE IF NOT EXISTS owners (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS organizations (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS sites (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (organization_id, name)
);

CREATE TABLE IF NOT EXISTS node_groups (
    id TEXT PRIMARY KEY,
    site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (site_id, name)
);
ALTER TABLE node_groups ALTER COLUMN site_id DROP NOT NULL;

CREATE TABLE IF NOT EXISTS enrollments (
    id TEXT PRIMARY KEY,
    group_id TEXT NOT NULL REFERENCES node_groups(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    max_uses INTEGER NOT NULL CHECK (max_uses > 0),
    uses INTEGER NOT NULL DEFAULT 0 CHECK (uses >= 0),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS nodes (
    id TEXT PRIMARY KEY,
    group_id TEXT NOT NULL REFERENCES node_groups(id) ON DELETE RESTRICT,
    remark TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('windows', 'linux')),
    machine_id TEXT NOT NULL DEFAULT '',
    agent_version TEXT NOT NULL,
    os_name TEXT NOT NULL DEFAULT '',
    os_version TEXT NOT NULL DEFAULT '',
    ip_address TEXT NOT NULL DEFAULT '',
    cpu_percent DOUBLE PRECISION NOT NULL DEFAULT 0,
    memory_percent DOUBLE PRECISION NOT NULL DEFAULT 0,
    disk_percent DOUBLE PRECISION NOT NULL DEFAULT 0,
    network_metrics_available BOOLEAN NOT NULL DEFAULT FALSE,
    network_upload_mb_s DOUBLE PRECISION NOT NULL DEFAULT 0,
    network_download_mb_s DOUBLE PRECISION NOT NULL DEFAULT 0,
    network_usage_available BOOLEAN NOT NULL DEFAULT FALSE,
    network_usage_day TEXT NOT NULL DEFAULT '',
    network_today_upload_bytes BIGINT NOT NULL DEFAULT 0,
    network_today_download_bytes BIGINT NOT NULL DEFAULT 0,
    network_month_upload_bytes BIGINT NOT NULL DEFAULT 0,
    network_month_download_bytes BIGINT NOT NULL DEFAULT 0,
    last_seen_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL
);
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS remark TEXT NOT NULL DEFAULT '';
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS network_metrics_available BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS network_upload_mb_s DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS network_download_mb_s DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS network_usage_available BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS network_usage_day TEXT NOT NULL DEFAULT '';
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS network_today_upload_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS network_today_download_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS network_month_upload_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS network_month_download_bytes BIGINT NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX IF NOT EXISTS nodes_machine_id_idx ON nodes(machine_id) WHERE machine_id <> '';
CREATE INDEX IF NOT EXISTS nodes_group_id_idx ON nodes(group_id);

CREATE TABLE IF NOT EXISTS node_credentials (
    node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS node_log_snapshots (
    node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    agent_log TEXT NOT NULL DEFAULT '',
    update_log TEXT NOT NULL DEFAULT '',
    captured_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS network_samples (
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    captured_at TIMESTAMPTZ NOT NULL,
    upload_mb_s DOUBLE PRECISION NOT NULL DEFAULT 0,
    download_mb_s DOUBLE PRECISION NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, captured_at)
);
CREATE INDEX IF NOT EXISTS network_samples_captured_at_idx ON network_samples(captured_at);

CREATE TABLE IF NOT EXISTS pairing_requests (
    id TEXT PRIMARY KEY,
    machine_id TEXT NOT NULL,
    hostname TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('windows', 'linux')),
    agent_version TEXT NOT NULL,
    credential_hash TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'approved', 'rejected', 'expired')),
    node_id TEXT REFERENCES nodes(id) ON DELETE SET NULL,
    group_id TEXT REFERENCES node_groups(id) ON DELETE SET NULL,
    remark TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    decided_at TIMESTAMPTZ
);
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'pairing_requests'
          AND column_name = 'existing_node_id'
    ) AND NOT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'pairing_requests'
          AND column_name = 'node_id'
    ) THEN
        ALTER TABLE pairing_requests RENAME COLUMN existing_node_id TO node_id;
    END IF;
END $$;
ALTER TABLE pairing_requests ADD COLUMN IF NOT EXISTS remark TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS pairing_requests_pending_idx ON pairing_requests(state, expires_at);
CREATE INDEX IF NOT EXISTS pairing_requests_created_at_idx ON pairing_requests(created_at);

CREATE TABLE IF NOT EXISTS command_tasks (
    id TEXT PRIMARY KEY,
    shell TEXT NOT NULL CHECK (shell IN ('powershell', 'cmd')),
    command TEXT NOT NULL,
    timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 10 AND 1800),
    created_by TEXT NOT NULL REFERENCES owners(id) ON DELETE RESTRICT,
    retried_from_id TEXT REFERENCES command_tasks(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS command_tasks_created_at_idx ON command_tasks(created_at DESC);

CREATE TABLE IF NOT EXISTS command_executions (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES command_tasks(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    status TEXT NOT NULL CHECK (status IN ('queued', 'leased', 'running', 'succeeded', 'failed', 'timed_out')),
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    lease_token_hash TEXT,
    lease_expires_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    exit_code INTEGER,
    output TEXT NOT NULL DEFAULT '',
    output_truncated BOOLEAN NOT NULL DEFAULT FALSE,
    error_message TEXT NOT NULL DEFAULT '',
    duration_ms BIGINT CHECK (duration_ms IS NULL OR duration_ms >= 0),
    UNIQUE (task_id, node_id)
);
CREATE INDEX IF NOT EXISTS command_executions_claim_idx
    ON command_executions(node_id, status, task_id);

CREATE TABLE IF NOT EXISTS events (
    id TEXT PRIMARY KEY,
    node_id TEXT REFERENCES nodes(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS events_created_at_idx ON events(created_at DESC);
