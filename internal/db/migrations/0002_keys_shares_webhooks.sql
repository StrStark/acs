-- Access keys authenticate S3 (SigV4) and REST API (Bearer / Basic) requests.
-- The secret must be recoverable for SigV4, so it is stored AES-GCM encrypted
-- with the server master key.
CREATE TABLE access_keys (
    id           TEXT    PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    secret_enc   BLOB    NOT NULL,
    -- read | readwrite | full
    permission   TEXT    NOT NULL DEFAULT 'readwrite',
    -- JSON array of bucket names; ["*"] means all buckets.
    buckets      TEXT    NOT NULL DEFAULT '["*"]',
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER,
    last_used_at INTEGER
);
CREATE INDEX access_keys_user_id ON access_keys(user_id);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE audit_log (
    id     INTEGER PRIMARY KEY,
    ts     INTEGER NOT NULL,
    actor  TEXT    NOT NULL,
    action TEXT    NOT NULL,
    target TEXT    NOT NULL DEFAULT '',
    ip     TEXT    NOT NULL DEFAULT '',
    detail TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_ts ON audit_log(ts);

CREATE TABLE shares (
    id               INTEGER PRIMARY KEY,
    token            TEXT    NOT NULL UNIQUE,
    -- file | folder | upload
    type             TEXT    NOT NULL,
    bucket           TEXT    NOT NULL,
    -- Object key for file shares; prefix (may be '') for folder and upload shares.
    key              TEXT    NOT NULL,
    created_by       INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at       INTEGER NOT NULL,
    expires_at       INTEGER,
    password_hash    TEXT,
    max_downloads    INTEGER,
    downloads        INTEGER NOT NULL DEFAULT 0,
    views            INTEGER NOT NULL DEFAULT 0,
    max_upload_bytes INTEGER,
    note             TEXT    NOT NULL DEFAULT '',
    disabled         INTEGER NOT NULL DEFAULT 0,
    last_accessed_at INTEGER
);
CREATE INDEX shares_bucket_key ON shares(bucket, key);

CREATE TABLE webhooks (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    url        TEXT    NOT NULL,
    secret_enc BLOB    NOT NULL,
    -- JSON array of event types; ["*"] means all.
    events     TEXT    NOT NULL,
    bucket     TEXT    NOT NULL DEFAULT '',
    prefix     TEXT    NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL
);

CREATE TABLE webhook_deliveries (
    id          INTEGER PRIMARY KEY,
    webhook_id  INTEGER NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
    event       TEXT    NOT NULL,
    payload     TEXT    NOT NULL,
    status      INTEGER NOT NULL DEFAULT 0,
    error       TEXT    NOT NULL DEFAULT '',
    attempt     INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);
CREATE INDEX webhook_deliveries_webhook ON webhook_deliveries(webhook_id, id);
