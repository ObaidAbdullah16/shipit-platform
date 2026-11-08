#!/usr/bin/env bash
# Demo: ship a broken release on purpose and watch ECS roll it back by itself.
# The current images are redeployed with CHAOS_FAIL_HEALTH=1, so the new tasks
# fail /healthz -> the deployment circuit breaker restores the previous revision.
# Users never see the broken version: the old task keeps serving the whole time.
# Takes ~5-8 minutes. Record your screen for the demo video.
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need aws jq curl
rel="$(release_json "$SSM_LAST_GOOD")"
[[ -n "$rel" ]] || die "Start ShipIt first: bash scripts/up.sh"
app_is_up || die "ShipIt is not running. Start it with: bash scripts/up.sh"
url="$(tf_out "$TF_APP" url)"

# Prove users are not affected: probe the site every 2 seconds in the background.
probe_log="$(mktemp)"
( while :; do
    code="$(curl -s -o /dev/null -m 3 -w '%{http_code}' "$url/" || echo 000)"
    echo "$(date +%T) $code" >> "$probe_log"; sleep 2
  done ) &
probe=$!
trap 'kill $probe 2>/dev/null; rm -f "$probe_log"' EXIT

start=$(date +%s)
if bash "$REPO_ROOT/scripts/deploy.sh" "$(jq -r .api <<<"$rel")" "$(jq -r .worker <<<"$rel")" "$(jq -r .version <<<"$rel")-chaos" --chaos; then
  die "The broken release was NOT rolled back - investigate with: bash scripts/status.sh"
fi
total=$(( $(date +%s) - start ))
kill $probe 2>/dev/null || true
ok=$(grep -c ' 200$' "$probe_log" || true); all=$(wc -l < "$probe_log")
log "Automatic rollback finished in ${total}s"
log "User-facing probes during the incident: ${ok}/${all} returned 200"
smoke_test "$url" "$(jq -r .version <<<"$rel")"
log "Check the 'shipit-unhealthy-targets' alarm and (if deployed) the IncidentScribe summary in Slack."
