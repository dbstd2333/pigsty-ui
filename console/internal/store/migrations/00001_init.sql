-- +goose Up
CREATE TABLE users (
    username      TEXT PRIMARY KEY,
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    last_login_at TEXT
);

CREATE TABLE sessions (
    token_hash   TEXT PRIMARY KEY,
    username     TEXT NOT NULL REFERENCES users (username) ON DELETE CASCADE,
    created_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    expires_at   TEXT NOT NULL
);

CREATE INDEX idx_sessions_username ON sessions (username);

-- +goose Down
DROP TABLE sessions;
DROP TABLE users;
