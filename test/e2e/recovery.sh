#!/usr/bin/env bash
# Disaster-recovery scenario against real containers:
#   machine A stores a bucket on a drive and is shut down; the drive moves to a
#   new mount path; a fresh machine B restores everything from that drive alone,
#   re-attaches it, and reports a blob that went missing.
set -euo pipefail

IMAGE="${ACS_IMAGE:-acs-server:dev}"
PORT="${ACS_DR_PORT:-18181}"
# Under $HOME so Docker Desktop can bind-mount it.
T="${ACS_DR_DIR:-$HOME/.cache/acs-dr-e2e}"
API="localhost:$PORT/api/v1"
PASS="dr-password-123"
PHRASE="correct horse battery staple"
JAR="$T/cookies"

ok() { echo "  ok   $1"; }
fail() { echo "  FAIL $1"; exit 1; }
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
j() { curl -sf -b "$JAR" -c "$JAR" -H 'Content-Type: application/json' "$@"; }

cleanup() { docker rm -f acs-dr-a acs-dr-b acs-dr-c >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
rm -rf "$T"
mkdir -p "$T/a" "$T/b" "$T/c" "$T/drives/disk1" "$T/other/travel"

start() { # name datadir [drives]
  docker run -d --rm --name "$1" --user "$(id -u):$(id -g)" -p "$PORT:8080" \
    -v "$2:/data" -v "${3:-$T/drives}:/drives" "$IMAGE" >/dev/null
  for _ in $(seq 100); do curl -sf "$API/health" >/dev/null && return; sleep 0.3; done
  docker logs "$1"; fail "$1 did not start"
}

echo "machine A"
start acs-dr-a "$T/a"
j -X POST "$API/setup" -d "{\"username\":\"admin\",\"password\":\"$PASS\"}" >/dev/null
KEY=$(j -X POST "$API/keys" -d '{"name":"dr","permission":"full"}')
KEY_ID=$(echo "$KEY" | json 'd["id"]')
KEY_SECRET=$(echo "$KEY" | json 'd["secret"]')
j -X PUT "$API/settings/recovery" -d "{\"passphrase\":\"$PHRASE\",\"currentPassword\":\"$PASS\"}" >/dev/null
DRIVE=$(j -X POST "$API/storage" -d '{"name":"Disk 1","type":"local","path":"/drives/disk1"}' | json 'd["id"]')
j -X POST "$API/buckets" -d "{\"name\":\"photos\",\"versioning\":true,\"backend\":\"$DRIVE\"}" >/dev/null
head -c 300000 /dev/urandom >"$T/cat.jpg"
curl -sf -b "$JAR" -X PUT --data-binary @"$T/cat.jpg" "$API/buckets/photos/objects/cat.jpg" >/dev/null
curl -sf -b "$JAR" -X PUT --data-binary "hello" "$API/buckets/photos/objects/hello.txt" >/dev/null
sleep 2
[ -f "$T/drives/disk1/.acs/manifest.json" ] && ok "metadata copied to the drive" || fail "no manifest on the drive"
docker stop acs-dr-a >/dev/null

echo "the drive moves to a new path; machine B starts empty"
mv "$T/drives/disk1" "$T/drives/usb-disk"
rm -f "$JAR"
start acs-dr-b "$T/b"
FOUND=$(curl -sf "$API/setup" | json 'd["restorable"][0]["path"]')
[ "$FOUND" = "/drives/usb-disk" ] && ok "setup offers restore from the drive" || fail "restorable: $FOUND"
code=$(curl -s -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' -X POST "$API/setup/restore" \
  -d "{\"path\":\"/drives/usb-disk\",\"passphrase\":\"wrong passphrase\"}")
[ "$code" = 422 ] && ok "wrong passphrase rejected" || fail "wrong passphrase: HTTP $code"
j -X POST "$API/setup/restore" -d "{\"path\":\"/drives/usb-disk\",\"passphrase\":\"$PHRASE\"}" >/dev/null
for _ in $(seq 100); do
  [ "$(curl -sf "$API/setup" 2>/dev/null | json 'd["needsSetup"]' 2>/dev/null)" = False ] && break
  sleep 0.3
done
ok "restored and restarted"

j -X POST "$API/auth/login" -d "{\"username\":\"admin\",\"password\":\"$PASS\"}" >/dev/null && ok "admin password restored" || fail "login"
curl -sf -H "Authorization: Bearer $KEY_ID:$KEY_SECRET" "$API/buckets" | grep -q '"photos"' && ok "access key secret restored" || fail "access key"
curl -sf -b "$JAR" "$API/buckets/photos/objects/cat.jpg" -o "$T/cat.out"
cmp -s "$T/cat.jpg" "$T/cat.out" && ok "object on the drive readable" || fail "object content"
[ "$(curl -sf -b "$JAR" "$API/buckets/photos/objects/hello.txt")" = hello ] && ok "inline object readable" || fail "inline object"
P=$(j "$API/storage" | json 'd["backends"][0]["path"]')
[ "$P" = "/drives/usb-disk" ] && ok "drive re-attached at its new path" || fail "drive path: $P"

echo "a blob goes missing"
find "$T/drives/usb-disk/blobs" -type f | head -1 | xargs rm
j -X POST "$API/alerts/check" -d '{"deep":false}' >/dev/null
for _ in $(seq 50); do
  [ "$(j "$API/alerts/summary" | json 'd["checking"]')" = False ] && break
  sleep 0.3
done
ALERTS=$(j "$API/alerts")
echo "$ALERTS" | json '[a["kind"] for a in d]' | grep -q "object.missing" && ok "missing object raised an alert" || fail "alerts: $ALERTS"
echo "$ALERTS" | json '[a.get("key") for a in d]' | grep -q "cat.jpg" && ok "alert names photos/cat.jpg" || fail "alerts: $ALERTS"
echo "$ALERTS" | json '[a["kind"] for a in d]' | grep -q "storage.foreign" && fail "restored server treats its own storage as foreign: $ALERTS" || ok "no false foreign-storage alert"
docker stop acs-dr-b >/dev/null

echo "a drive from another ACS server is imported"
rm -f "$JAR"
start acs-dr-c "$T/c" "$T/other"
j -X POST "$API/setup" -d '{"username":"admin","password":"other-password-1"}' >/dev/null
TRAVEL=$(j -X POST "$API/storage" -d '{"name":"Travel disk","type":"local","path":"/drives/travel"}' | json 'd["id"]')
j -X POST "$API/buckets" -d "{\"name\":\"trip\",\"versioning\":false,\"backend\":\"$TRAVEL\"}" >/dev/null
head -c 200000 /dev/urandom >"$T/beach.jpg"
curl -sf -b "$JAR" -X PUT --data-binary @"$T/beach.jpg" "$API/buckets/trip/objects/beach.jpg" >/dev/null
sleep 2
docker stop acs-dr-c >/dev/null
mv "$T/other/travel" "$T/drives/travel"
rm -f "$JAR"
start acs-dr-b "$T/b"
j -X POST "$API/auth/login" -d "{\"username\":\"admin\",\"password\":\"$PASS\"}" >/dev/null
STATE=$(j "$API/storage/discover" | json '[x["state"] for x in d if x["path"]=="/drives/travel"][0]')
[ "$STATE" = foreign ] && ok "drive discovered as foreign" || fail "discover state: $STATE"
j "$API/alerts" | json '[a["kind"] for a in d]' | grep -q "storage.foreign" && fail "foreign alert before attaching" || true
R=$(j -X POST "$API/storage/import" -d '{"path":"/drives/travel"}')
[ "$(echo "$R" | json 'd["imported"]')" = "['trip']" ] && ok "bucket imported" || fail "import: $R"
curl -sf -b "$JAR" "$API/buckets/trip/objects/beach.jpg" -o "$T/beach.out"
cmp -s "$T/beach.jpg" "$T/beach.out" && ok "imported object readable" || fail "imported object content"
STATE=$(j "$API/storage/discover" | json '[x["state"] for x in d if x["path"]=="/drives/travel"][0]')
[ "$STATE" = attached ] && ok "drive now owned by this server" || fail "discover state after import: $STATE"
echo "Recovery e2e passed."
