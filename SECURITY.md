# Security policy

Please **do not** open public issues for security vulnerabilities.

Report them privately through GitHub's [private vulnerability reporting](https://github.com/StrStark/acs/security/advisories/new). Include steps to reproduce and the affected version. You should get a response within a few days, and a fix or mitigation plan as soon as the issue is confirmed.

## Hardening checklist

- Finish first-run setup immediately, or set `ACS_SETUP_TOKEN` if the panel is reachable by others.
- Serve the panel and S3 API over HTTPS (reverse proxy) and set `ACS_COOKIE_SECURE=true`.
- Back up `master.key` separately from the data; it encrypts stored access-key and webhook secrets.
- Prefer narrowly scoped access keys (read-only, specific buckets, expiry).
