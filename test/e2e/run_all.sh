#!/usr/bin/env bash
# Builds the image, starts a throwaway server, creates an admin and access key
# through the REST API, then runs every S3 client suite against it.
#
#   test/e2e/run_all.sh            # all suites
#   SKIP_BUILD=1 test/e2e/run_all.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

NAME=acs-e2e
PANEL_PORT=${PANEL_PORT:-18080}
S3_PORT=${S3_PORT:-19000}

[ -n "${SKIP_BUILD:-}" ] || docker build -q -t acs-server:dev . >/dev/null
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --rm --name "$NAME" -p "$PANEL_PORT:8080" -p "$S3_PORT:9000" acs-server:dev >/dev/null
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT
until curl -sf "localhost:$PANEL_PORT/api/v1/health" >/dev/null; do sleep 0.3; done

API="localhost:$PANEL_PORT/api/v1"
JAR=$(mktemp)
curl -sf -c "$JAR" -H 'Content-Type: application/json' -X POST "$API/setup" \
  -d '{"username":"admin","password":"e2e-password-123"}' >/dev/null
KEY=$(curl -sf -b "$JAR" -H 'Content-Type: application/json' -X POST "$API/keys" -d '{"name":"e2e","permission":"full"}')
rm -f "$JAR"
export AWS_ACCESS_KEY_ID=$(echo "$KEY" | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')
export AWS_SECRET_ACCESS_KEY=$(echo "$KEY" | python3 -c 'import json,sys;print(json.load(sys.stdin)["secret"])')
export ACS_S3="http://localhost:$S3_PORT"

test/e2e/s3_awscli.sh
docker run --rm --network host -v "$PWD/test/e2e:/t" -e ACS_S3 -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
  python:3.12-slim sh -c '
    if ! pip install -q --disable-pip-version-check --root-user-action=ignore boto3 2>/dev/null; then
      echo "boto3: SKIPPED (could not install boto3 from PyPI)"; exit 0
    fi
    python /t/s3_boto3.py'
test/e2e/s3_rclone.sh
echo "All e2e suites passed."
