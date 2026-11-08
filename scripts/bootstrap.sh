#!/usr/bin/env bash
# Create (or update) the long-lived part of ShipIt: the two ECR repositories.
# Idempotent and almost free. The release workflow runs it automatically.
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need aws terraform
tf_init "$TF_BOOT" "shipit-platform/bootstrap.tfstate"
tf_apply "$TF_BOOT" >/dev/null
log "ECR repositories ready: $(tf_out "$TF_BOOT" api_repository_url), $(tf_out "$TF_BOOT" worker_repository_url)"
