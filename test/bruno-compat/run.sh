#!/bin/sh
# Bruno compatibility check. Needs Go and Node.
#
# 1. syncs the sample fixture into a fresh collection
# 2. round trips every file through Bruno's own parser and serializer
#    (what the Bruno app does on save) and fails on any semantic difference
# 3. does the same for a collection produced by the merge scenario test,
#    so files rewritten after a merge (with user scripts) are covered too
# 4. runs the fresh collection with the Bruno CLI against a local echo server
#    and checks that headers, query strings and bodies really go out
#
# Run it after changing the generator and whenever the team updates Bruno
# (bump the versions in package.json first).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/brunosync-compat.XXXXXX")
echo "work dir: $work"

echo "== installing Bruno packages"
(cd "$here" && npm install --silent --no-audit --no-fund)

echo "== building brunosync"
(cd "$root" && go build -o "$work/brunosync" ./cmd/brunosync)

cat > "$work/brunosync.yml" <<EOF
version: 1
collection:
  path: ./collection
  name: Compat
environments:
  - name: staging
    defaults: { host: 127.0.0.1 }
    services:
      orders-service: { port: "8082" }
repos:
  - path: $root/test/fixtures/sample-repo
EOF
"$work/brunosync" sync -c "$work/brunosync.yml" -q

echo "== round trip: fresh collection"
node "$here/roundtrip.js" "$work/collection" "$work/collection-saved"
"$work/brunosync" debug compare "$work/collection" "$work/collection-saved"

echo "== round trip: collection after a merge with user edits"
(cd "$root" && BRUNOSYNC_KEEP_COLLECTION="$work/merged" go test ./internal/syncer -run TestMergeScenario -count=1 >/dev/null)
node "$here/roundtrip.js" "$work/merged" "$work/merged-saved"
"$work/brunosync" debug compare "$work/merged" "$work/merged-saved"

echo "== bru run against an echo server"
node "$here/echo.js" "$work/echo.log" &
echo_pid=$!
trap 'kill $echo_pid 2>/dev/null || true' EXIT
sleep 1
(cd "$work/collection" && "$here/node_modules/.bin/bru" run -r >"$work/bru.log" 2>&1) || {
	cat "$work/bru.log"
	echo "bru run failed"
	exit 1
}

fail=0
expect() {
	if grep -q "$1" "$work/echo.log"; then
		echo "  ok: $2"
	else
		echo "  MISSING: $2"
		fail=1
	fi
}
expect '"url":"/api/v1/cost-tree?depth=2"' "required query param sent"
expect '"tenant":"okapi"' "header from the common module sent"
expect '"body":"orderId=ord-42"' "form body sent"
expect 'SAT-BUS-01' "generated JSON body sent"
expect '"port":8081' "orders base URL built from host and port variables"
if grep -q '.brunosync' "$work/bru.log"; then
	echo "  FAIL: bru ran files from .brunosync"
	fail=1
fi
requests=$(wc -l <"$work/echo.log" | tr -d ' ')
echo "  $requests requests received"

echo "== bru run --env staging (environment overrides the orders port)"
: >"$work/echo.log"
(cd "$work/collection" && "$here/node_modules/.bin/bru" run -r --env staging >"$work/bru-env.log" 2>&1) || {
	cat "$work/bru-env.log"
	echo "bru run --env staging failed"
	exit 1
}
expect '"port":8082,"method":"[A-Z]*","url":"/orders' "orders requests follow the staging port"
if grep -q '"port":8081' "$work/echo.log"; then
	echo "  FAIL: a request still went to the spec port 8081"
	fail=1
fi
exit $fail
