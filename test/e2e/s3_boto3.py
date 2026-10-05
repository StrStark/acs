"""End-to-end S3 compatibility test using boto3 (run via run_all.sh)."""

import hashlib
import io
import os
import sys
import time
import urllib.request

import boto3
from boto3.s3.transfer import TransferConfig
from botocore.config import Config
from botocore.exceptions import ClientError

endpoint = os.environ["ACS_S3"]
s3 = boto3.client(
    "s3",
    endpoint_url=endpoint,
    region_name="us-east-1",
    config=Config(signature_version="s3v4", s3={"addressing_style": "path"}),
)
bucket = f"boto-{int(time.time())}"
passed = 0


def ok(msg):
    global passed
    passed += 1
    print(f"  \033[32m✓\033[0m {msg}")


def expect_error(code, fn, *args, **kwargs):
    try:
        fn(*args, **kwargs)
    except ClientError as e:
        got = e.response["Error"]["Code"]
        assert got == code, f"expected {code}, got {got}"
        return
    raise AssertionError(f"expected {code}, call succeeded")


print(f"boto3 {boto3.__version__} against {endpoint} (bucket {bucket})")

s3.create_bucket(Bucket=bucket)
ok("create bucket")
expect_error("BucketAlreadyOwnedByYou", s3.create_bucket, Bucket=bucket)
ok("duplicate bucket rejected")
expect_error("NoSuchBucket", s3.list_objects_v2, Bucket="does-not-exist-xyz")
ok("NoSuchBucket error code")

for alg in ["CRC32", "SHA1", "SHA256"]:  # CRC32C needs botocore[crt]
    s3.put_object(Bucket=bucket, Key=f"checksum/{alg}.txt", Body=b"checksummed " + alg.encode(), ChecksumAlgorithm=alg)
ok("put with CRC32/SHA1/SHA256 checksums")

body = s3.get_object(Bucket=bucket, Key="checksum/CRC32.txt")["Body"].read()
assert body == b"checksummed CRC32"
ok("get object")
expect_error("NoSuchKey", s3.get_object, Bucket=bucket, Key="nope")
ok("NoSuchKey error code")

data = os.urandom(12 * 1024 * 1024 + 123)
s3.upload_fileobj(io.BytesIO(data), bucket, "multi/big.bin",
                  Config=TransferConfig(multipart_threshold=5 * 1024 * 1024, multipart_chunksize=5 * 1024 * 1024))
out = io.BytesIO()
s3.download_fileobj(bucket, "multi/big.bin", out,
                    Config=TransferConfig(multipart_threshold=5 * 1024 * 1024, multipart_chunksize=5 * 1024 * 1024))
assert hashlib.sha256(out.getvalue()).digest() == hashlib.sha256(data).digest()
ok("managed multipart upload + parallel ranged download")

head = s3.head_object(Bucket=bucket, Key="multi/big.bin")
assert head["ETag"].strip('"').endswith("-3"), head["ETag"]
ok("multipart ETag format")

s3.put_object(Bucket=bucket, Key="cond.txt", Body=b"one", IfNoneMatch="*")
expect_error("PreconditionFailed", s3.put_object, Bucket=bucket, Key="cond.txt", Body=b"two", IfNoneMatch="*")
ok("conditional write If-None-Match: *")

etag = s3.head_object(Bucket=bucket, Key="cond.txt")["ETag"]
try:
    s3.get_object(Bucket=bucket, Key="cond.txt", IfNoneMatch=etag)
    raise AssertionError("expected 304")
except ClientError as e:
    assert e.response["Error"]["Code"] == "304"
ok("conditional GET returns 304")

for i in range(25):
    s3.put_object(Bucket=bucket, Key=f"page/{i:03d}", Body=b"x")
keys = []
for page in s3.get_paginator("list_objects_v2").paginate(Bucket=bucket, Prefix="page/", PaginationConfig={"PageSize": 7}):
    keys += [o["Key"] for o in page.get("Contents", [])]
assert keys == [f"page/{i:03d}" for i in range(25)], keys
ok("ListObjectsV2 pagination")
keys = []
for page in s3.get_paginator("list_objects").paginate(Bucket=bucket, Prefix="page/", PaginationConfig={"PageSize": 10}):
    keys += [o["Key"] for o in page.get("Contents", [])]
assert len(keys) == 25
ok("ListObjects v1 pagination")

s3.copy_object(Bucket=bucket, Key="copied.txt", CopySource={"Bucket": bucket, "Key": "cond.txt"},
               MetadataDirective="REPLACE", Metadata={"Color": "Blue"}, ContentType="text/plain")
h = s3.head_object(Bucket=bucket, Key="copied.txt")
assert h["Metadata"] == {"color": "Blue"} and h["ContentType"] == "text/plain", h
ok("copy with REPLACE metadata")
expect_error("InvalidRequest", s3.copy_object, Bucket=bucket, Key="cond.txt", CopySource={"Bucket": bucket, "Key": "cond.txt"})
ok("copy-to-self without changes rejected")

url = s3.generate_presigned_url("put_object", Params={"Bucket": bucket, "Key": "presigned.txt"}, ExpiresIn=60)
req = urllib.request.Request(url, data=b"via presigned put", method="PUT")
urllib.request.urlopen(req).read()
assert s3.get_object(Bucket=bucket, Key="presigned.txt")["Body"].read() == b"via presigned put"
ok("presigned PUT upload")

cors = {"CORSRules": [{"AllowedOrigins": ["https://app.example.com"], "AllowedMethods": ["GET", "PUT"],
                       "AllowedHeaders": ["*"], "MaxAgeSeconds": 600}]}
s3.put_bucket_cors(Bucket=bucket, CORSConfiguration=cors)
assert s3.get_bucket_cors(Bucket=bucket)["CORSRules"][0]["AllowedOrigins"] == ["https://app.example.com"]
pre = urllib.request.Request(f"{endpoint}/{bucket}/presigned.txt", method="OPTIONS", headers={
    "Origin": "https://app.example.com", "Access-Control-Request-Method": "PUT"})
resp = urllib.request.urlopen(pre)
assert resp.headers["Access-Control-Allow-Origin"] == "https://app.example.com"
ok("bucket CORS config + preflight")

s3.put_bucket_lifecycle_configuration(Bucket=bucket, LifecycleConfiguration={"Rules": [
    {"ID": "logs", "Status": "Enabled", "Filter": {"Prefix": "logs/"}, "Expiration": {"Days": 30}}]})
rules = s3.get_bucket_lifecycle_configuration(Bucket=bucket)["Rules"]
assert rules[0]["Expiration"]["Days"] == 30
ok("lifecycle configuration")

s3.put_bucket_versioning(Bucket=bucket, VersioningConfiguration={"Status": "Enabled"})
v1 = s3.put_object(Bucket=bucket, Key="ver.txt", Body=b"v1")["VersionId"]
v2 = s3.put_object(Bucket=bucket, Key="ver.txt", Body=b"v2")["VersionId"]
assert s3.get_object(Bucket=bucket, Key="ver.txt", VersionId=v1)["Body"].read() == b"v1"
d = s3.delete_object(Bucket=bucket, Key="ver.txt")
assert d["DeleteMarker"]
s3.delete_object(Bucket=bucket, Key="ver.txt", VersionId=d["VersionId"])
assert s3.get_object(Bucket=bucket, Key="ver.txt")["Body"].read() == b"v2"
ok("versioning: get by version, delete marker, undelete")

s3.put_object_tagging(Bucket=bucket, Key="cond.txt", Tagging={"TagSet": [{"Key": "a", "Value": "1"}]})
assert s3.get_object_tagging(Bucket=bucket, Key="cond.txt")["TagSet"] == [{"Key": "a", "Value": "1"}]
ok("object tagging")

# Clean up every version, then the bucket.
for page in s3.get_paginator("list_object_versions").paginate(Bucket=bucket):
    objs = [{"Key": v["Key"], "VersionId": v["VersionId"]} for v in page.get("Versions", []) + page.get("DeleteMarkers", [])]
    if objs:
        s3.delete_objects(Bucket=bucket, Delete={"Objects": objs, "Quiet": True})
s3.delete_bucket(Bucket=bucket)
ok("delete all versions + bucket")

print(f"boto3: {passed} checks passed")
sys.exit(0)
