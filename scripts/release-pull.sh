#!/usr/bin/env bash
# For AWS accounts that GitHub cannot log in to (new AWS sign-up "projects"): take the newest
# signed release that the Release workflow pushed to ghcr.io, copy it into ECR (same digests,
# signatures and SBOMs included) and roll it out - with the same automatic rollback as always.
#
#   git push (a change under services/)   ->  GitHub: CI, build, scan, sign (~10 minutes)
#   bash scripts/release-pull.sh           ->  laptop: verify, copy to ECR, deploy, smoke test
#
# If ShipIt is stopped, the release is only recorded; the next `bash scripts/up.sh` starts it.
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need aws terraform jq curl docker cosign

project_account || warn "AWS_ACCOUNT_TYPE is not 'project': in a classic account the Release workflow deploys by itself."
import_release_from_ghcr
rel="$(release_json "$SSM_LATEST")"
api="$(jq -r .api <<<"$rel")"; worker="$(jq -r .worker <<<"$rel")"; version="$(jq -r .version <<<"$rel")"
if app_is_up; then
  bash "$REPO_ROOT/scripts/deploy.sh" "$api" "$worker" "$version"
else
  log "ShipIt is stopped - release $version is recorded; the next 'bash scripts/up.sh' starts it."
fi
