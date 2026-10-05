#!/usr/bin/env bash
# End-to-end test with rclone (S3 provider "Other").
set -euo pipefail
: "${ACS_S3:?}" "${AWS_ACCESS_KEY_ID:?}" "${AWS_SECRET_ACCESS_KEY:?}"

WORK="$(cd "$(dirname "$0")" && pwd)/.work/rclone-$$"
mkdir -p "$WORK/src/a/b"
trap 'rm -rf "$WORK" 2>/dev/null || true' EXIT
for i in $(seq 1 20); do head -c $((RANDOM * 10)) /dev/urandom > "$WORK/src/f$i.bin"; done
head -c 9000000 /dev/urandom > "$WORK/src/a/b/large.bin"
echo "unicode name" > "$WORK/src/a/héllo wörld (1).txt"

rc() {
  docker run --rm --network host --user "$(id -u):$(id -g)" -e HOME=/tmp -e RCLONE_CONFIG=/tmp/rclone.conf -v "$WORK:/work" \
    -e RCLONE_CONFIG_ACS_TYPE=s3 -e RCLONE_CONFIG_ACS_PROVIDER=Other \
    -e RCLONE_CONFIG_ACS_ENDPOINT="$ACS_S3" -e RCLONE_CONFIG_ACS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" \
    -e RCLONE_CONFIG_ACS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" -e RCLONE_CONFIG_ACS_FORCE_PATH_STYLE=true \
    rclone/rclone "$@"
}
B="rclone-$(date +%s)"
echo "rclone against $ACS_S3 (bucket $B)"
rc mkdir "acs:$B"
rc sync /work/src "acs:$B/data" --s3-upload-cutoff 5M --s3-chunk-size 5M
echo "  ✓ sync up (incl. multipart and unicode names)"
rc check /work/src "acs:$B/data" --download
echo "  ✓ check --download (content verified)"
rc sync "acs:$B/data" /work/dst
diff -r "$WORK/src" "$WORK/dst"
echo "  ✓ sync down matches"
rc purge "acs:$B"
echo "  ✓ purge bucket"
echo "rclone: all checks passed"
