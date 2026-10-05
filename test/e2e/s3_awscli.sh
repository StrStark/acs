#!/usr/bin/env bash
# End-to-end S3 compatibility test using the official AWS CLI (in Docker).
#
#   ACS_S3=http://localhost:9000 AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... test/e2e/s3_awscli.sh
#
# Requires Docker; the CLI container uses host networking.
set -euo pipefail

: "${ACS_S3:?set ACS_S3 to the S3 endpoint, e.g. http://localhost:9000}"
: "${AWS_ACCESS_KEY_ID:?}"
: "${AWS_SECRET_ACCESS_KEY:?}"

# Docker Desktop only shares paths under $HOME, so keep scratch files in the repo.
WORK="$(cd "$(dirname "$0")" && pwd)/.work/$$"
mkdir -p "$WORK"
trap 'rm -rf "$WORK"' EXIT
BUCKET="e2e-$(date +%s)"
pass=0

aws() {
  docker run --rm --network host -v "$WORK:/work" -w /work \
    -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_DEFAULT_REGION=us-east-1 \
    amazon/aws-cli --endpoint-url "$ACS_S3" "$@"
}
ok() { pass=$((pass + 1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '  \033[31m✗ %s\033[0m\n' "$1"; exit 1; }

echo "AWS CLI against $ACS_S3 (bucket $BUCKET)"

aws s3 mb "s3://$BUCKET" >/dev/null && ok "create bucket" || fail "create bucket"
aws s3 ls | grep "$BUCKET" >/dev/null && ok "list buckets" || fail "list buckets"

echo "hello world" > "$WORK/small.txt"
head -c 23000000 /dev/urandom > "$WORK/big.bin"
mkdir -p "$WORK/tree/sub"
for i in 1 2 3; do echo "file $i" > "$WORK/tree/f$i.txt"; done
echo nested > "$WORK/tree/sub/n.txt"

aws s3 cp small.txt "s3://$BUCKET/docs/small.txt" >/dev/null && ok "put small object" || fail "put small object"
aws s3 cp big.bin "s3://$BUCKET/big.bin" >/dev/null && ok "multipart upload (23 MB)" || fail "multipart upload (23 MB)"
aws s3 cp "s3://$BUCKET/big.bin" big.down >/dev/null
cmp -s "$WORK/big.bin" "$WORK/big.down" && ok "download matches upload" || fail "big.bin content mismatch"
aws s3 cp "s3://$BUCKET/docs/small.txt" - | grep "hello world" >/dev/null && ok "get small object to stdout" || fail "get small object to stdout"

aws s3 sync tree "s3://$BUCKET/tree" >/dev/null && ok "sync directory" || fail "sync directory"
[ "$(aws s3 ls "s3://$BUCKET/tree/" --recursive | wc -l)" -eq 4 ] && ok "recursive listing" || fail "recursive listing count"
aws s3 ls "s3://$BUCKET/" | grep "PRE tree/" >/dev/null && ok "delimiter listing shows prefixes" || fail "delimiter listing shows prefixes"

aws s3api head-object --bucket "$BUCKET" --key big.bin | grep '"ContentLength": 23000000' >/dev/null && ok "head-object size" || fail "head-object size"
aws s3api put-object --bucket "$BUCKET" --key meta.txt --body small.txt --content-type text/plain \
  --metadata author=e2e --tagging 'env=test' >/dev/null && ok "put-object with metadata and tags" || fail "put-object with metadata and tags"
aws s3api head-object --bucket "$BUCKET" --key meta.txt | grep '"author": "e2e"' >/dev/null && ok "metadata round-trip" || fail "metadata round-trip"
aws s3api get-object-tagging --bucket "$BUCKET" --key meta.txt | grep '"Value": "test"' >/dev/null && ok "tagging round-trip" || fail "tagging round-trip"
aws s3api get-object --bucket "$BUCKET" --key big.bin --range bytes=100-199 range.out >/dev/null
[ "$(stat -c %s "$WORK/range.out")" -eq 100 ] && cmp -s <(tail -c +101 "$WORK/big.bin" | head -c 100) "$WORK/range.out" \
  && ok "ranged GET" || fail "ranged GET"

aws s3 cp "s3://$BUCKET/docs/small.txt" "s3://$BUCKET/copy/small.txt" >/dev/null && ok "server-side copy" || fail "server-side copy"
aws s3 mv "s3://$BUCKET/copy/small.txt" "s3://$BUCKET/moved/small.txt" >/dev/null && ok "move" || fail "move"

URL=$(aws s3 presign "s3://$BUCKET/docs/small.txt" --expires-in 60)
curl -sf "$URL" | grep "hello world" >/dev/null && ok "presigned GET URL" || fail "presigned GET URL"

aws s3api put-bucket-versioning --bucket "$BUCKET" --versioning-configuration Status=Enabled && ok "enable versioning" || fail "enable versioning"
echo v1 > "$WORK/v.txt"; aws s3 cp v.txt "s3://$BUCKET/v.txt" >/dev/null
echo v2 > "$WORK/v.txt"; aws s3 cp v.txt "s3://$BUCKET/v.txt" >/dev/null
[ "$(aws s3api list-object-versions --bucket "$BUCKET" --prefix v.txt --query 'length(Versions)')" = "2" ] \
  && ok "list object versions" || fail "list object versions"
aws s3 rm "s3://$BUCKET/v.txt" >/dev/null
aws s3api list-object-versions --bucket "$BUCKET" --prefix v.txt | grep DeleteMarkers >/dev/null && ok "delete marker created" || fail "delete marker created"

aws s3api delete-objects --bucket "$BUCKET" --delete '{"Objects":[{"Key":"meta.txt"},{"Key":"moved/small.txt"}]}' >/dev/null \
  && ok "batch delete" || fail "batch delete"
aws s3 rm "s3://$BUCKET" --recursive >/dev/null && ok "recursive delete" || fail "recursive delete"

# Remove all versions so the bucket can be deleted.
aws s3api list-object-versions --bucket "$BUCKET" --output json --query '{Objects: [Versions[].{Key:Key,VersionId:VersionId}, DeleteMarkers[].{Key:Key,VersionId:VersionId}][]}' > "$WORK/vers.json"
aws s3api delete-objects --bucket "$BUCKET" --delete file:///work/vers.json >/dev/null
aws s3 rb "s3://$BUCKET" >/dev/null && ok "delete bucket" || fail "delete bucket"

echo "AWS CLI: $pass checks passed"
