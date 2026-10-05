-- Timestamps are unix seconds.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL DEFAULT 'admin',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

-- Session tokens are never stored; only their SHA-256 hash.
CREATE TABLE sessions (
    token_hash   BLOB    PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    ip           TEXT    NOT NULL DEFAULT '',
    user_agent   TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_id    ON sessions(user_id);
CREATE INDEX sessions_expires_at ON sessions(expires_at);
