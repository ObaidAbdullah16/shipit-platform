#!/usr/bin/env bash
# Manually roll back to the last release that passed its smoke test in AWS.
# (Failed deployments are rolled back automatically by the ECS circuit breaker;
# this is for "it deployed fine but something is wrong" situations.)
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need aws jq
rel="$(release_json "$SSM_LAST_GOOD")"
[[ -n "$rel" ]] || die "No last-good release recorded yet"
log "Rolling back to $(jq -r .version <<<"$rel") (recorded $(jq -r .time <<<"$rel"))"
bash "$REPO_ROOT/scripts/deploy.sh" "$(jq -r .api <<<"$rel")" "$(jq -r .worker <<<"$rel")" "$(jq -r .version <<<"$rel")"
