#!/bin/sh
# Runs the real world suite: builds brunosync, installs the pinned Bruno CLI,
# then syncs and checks every repo in repos.yml. Arguments go to suite.js,
# for example: test/realworld/run.sh --only stripe,github --no-run
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
(cd "$root/test/bruno-compat" && npm install --silent --no-audit --no-fund)
(cd "$root" && make build >/dev/null)
exec node "$here/suite.js" "$@"
