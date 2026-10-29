-- Problems that need attention: missing or corrupt objects, offline storage,
-- failed metadata replication. A fingerprint deduplicates repeated reports.
CREATE TABLE alerts (
    id          INTEGER PRIMARY KEY,
    fingerprint TEXT    NOT NULL UNIQUE,
    kind        TEXT    NOT NULL,
    severity    TEXT    NOT NULL,
    title       TEXT    NOT NULL,
    detail      TEXT    NOT NULL DEFAULT '',
    bucket      TEXT    NOT NULL DEFAULT '',
    object_key  TEXT    NOT NULL DEFAULT '',
    backend     TEXT    NOT NULL DEFAULT '',
    count       INTEGER NOT NULL DEFAULT 1,
    first_seen  INTEGER NOT NULL,
    last_seen   INTEGER NOT NULL,
    resolved_at INTEGER
);
CREATE INDEX alerts_open ON alerts(resolved_at, last_seen);
