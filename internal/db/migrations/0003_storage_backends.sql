-- Storage backends: attached drives and remote S3-compatible providers.
CREATE TABLE storage_backends (
    id         TEXT    PRIMARY KEY,
    name       TEXT    NOT NULL,
    -- local | s3
    type       TEXT    NOT NULL,
    -- Non-secret settings as JSON (path, endpoint, bucket, prefix, ...).
    config     TEXT    NOT NULL,
    -- S3 secret key, AES-GCM encrypted with the master key.
    secret_enc BLOB,
    created_at INTEGER NOT NULL
);
