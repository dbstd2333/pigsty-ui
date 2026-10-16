-- +goose Up
CREATE TABLE jobs (
    id             TEXT PRIMARY KEY,
    action         TEXT NOT NULL,
    title          TEXT NOT NULL,
    params         TEXT NOT NULL,
    check_flag     INTEGER NOT NULL DEFAULT 0,
    risk           TEXT NOT NULL,
    status         TEXT NOT NULL,
    plan           TEXT NOT NULL DEFAULT '[]',
    commands       TEXT NOT NULL DEFAULT '[]',
    cluster        TEXT,
    targets        TEXT NOT NULL DEFAULT '[]',
    config_hash    TEXT NOT NULL,
    config_commit  TEXT,
    username       TEXT NOT NULL,
    confirm_text   TEXT,
    created_at     TEXT NOT NULL,
    started_at     TEXT,
    finished_at    TEXT,
    exit_code      INTEGER,
    current_step   INTEGER NOT NULL DEFAULT 0,
    stats          TEXT NOT NULL DEFAULT '{}',
    restore_point  TEXT,
    error          TEXT,
    runner         TEXT,
    pid            INTEGER,
    unit           TEXT
);

CREATE INDEX idx_jobs_status ON jobs (status);
CREATE INDEX idx_jobs_cluster ON jobs (cluster);
CREATE INDEX idx_jobs_created ON jobs (created_at);

CREATE TABLE applied (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    cluster TEXT NOT NULL,
    action  TEXT NOT NULL,
    commit_hash TEXT NOT NULL,
    job_id  TEXT,
    at      TEXT NOT NULL
);

CREATE INDEX idx_applied_cluster ON applied (cluster, at);

CREATE TABLE restore_points (
    name       TEXT PRIMARY KEY,
    lsn        TEXT,
    time       TEXT NOT NULL,
    job_id     TEXT,
    cluster    TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX idx_restore_points_cluster ON restore_points (cluster, time);

CREATE TABLE node_specs (
    ip         TEXT PRIMARY KEY,
    cpu        INTEGER NOT NULL,
    mem_mb     INTEGER NOT NULL,
    disk_gb    INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE settings;
DROP TABLE node_specs;
DROP TABLE restore_points;
DROP TABLE applied;
DROP TABLE jobs;
