# Contributing to ACS

Thanks for helping! Bug reports, S3 compatibility reports, docs fixes and features are all welcome.

## Getting started

You only need Docker (Node.js 24+ is handy for UI work).

```bash
git clone https://github.com/StrStark/acs && cd acs
docker compose up -d --build              # full stack on :8080 (panel) and :9000 (S3)
cd web && npm install && npm run dev      # hot-reloading panel, proxies /api to :8080
```

## Before you open a pull request

```bash
# Go unit + integration tests (no local Go needed)
docker run --rm -v "$PWD":/src -w /src golang:1.27-alpine go test ./...

# Type-check and build the panel
cd web && npm run build

# Real-client S3 compatibility suites (AWS CLI, boto3, rclone)
test/e2e/run_all.sh
```

- Keep changes focused; one topic per PR.
- Match the surrounding code style (`gofmt`, existing naming and comment density).
- Add tests for storage-engine and API behaviour changes. If you fix an S3 compatibility bug, add a check to `test/e2e`.

## Where things live

| Area | Path |
|---|---|
| Storage engine (buckets, objects, versions, multipart, lifecycle) | `internal/object` |
| S3 protocol (SigV4, routing, XML) | `internal/s3` |
| REST API + OpenAPI spec | `internal/api` |
| Users, roles, access keys | `internal/auth` |
| Web panel | `web/src` |

## Reporting S3 incompatibilities

Please include the client and version, the exact command or SDK call, and the server log line (`ACS_LOG_LEVEL=debug`). These reports are the fastest way to make ACS work with more tools.
