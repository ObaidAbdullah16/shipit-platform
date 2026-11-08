#!/usr/bin/env bash
# Generate traffic so the dashboard and alarms have something to show.
#   bash scripts/loadtest.sh [seconds] [concurrency]
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need python3
url="${URL:-}"
if [[ -z "$url" ]]; then
  app_is_up || die "ShipIt is not running (or set URL=http://localhost:8080 for docker compose)"
  url="$(tf_out "$TF_APP" url)"
fi
python3 "$REPO_ROOT/tools/loadgen.py" "$url" --duration "${1:-60}" --concurrency "${2:-8}"
