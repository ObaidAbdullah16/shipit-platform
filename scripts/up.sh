#!/usr/bin/env bash
# Start ShipIt in AWS: VPC, load balancer, ECS Fargate service, RDS PostgreSQL,
# alarms and a dashboard, running the newest signed release.
# Takes ~10-12 minutes. Cost while up: ~$0.10/hour. Stop with scripts/down.sh.
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need aws terraform jq curl

bash "$REPO_ROOT/scripts/bootstrap.sh"

ensure_release
rel="$(release_json "$SSM_LATEST")"
api="$(jq -r .api <<<"$rel")"; worker="$(jq -r .worker <<<"$rel")"; version="$(jq -r .version <<<"$rel")"
log "Release $version"
verify_signature "$api"
verify_signature "$worker"

log "Terraform apply (takes ~10 minutes the first time: RDS and the load balancer are slow to create)"
tf_init "$TF_APP" "shipit-platform/app.tfstate"
tf_apply "$TF_APP" -var "api_image=$api" -var "worker_image=$worker"
url="$(tf_out "$TF_APP" url)"

# Already running an older release? Roll the newest one out the normal way.
running="$(aws ecs describe-task-definition --task-definition "$(aws ecs describe-services \
  --cluster "$(tf_out "$TF_APP" cluster_name)" --services "$(tf_out "$TF_APP" service_name)" \
  --query 'services[0].taskDefinition' --output text)" \
  --query "taskDefinition.containerDefinitions[?name=='api'].image | [0]" --output text)"
if [[ "$running" != "$api" ]]; then
  log "Service runs an older release - deploying $version"
  bash "$REPO_ROOT/scripts/deploy.sh" "$api" "$worker" "$version"
else
  smoke_test "$url" "$version" || die "The new stack is not healthy - check: bash scripts/status.sh"
  put_release "$SSM_LAST_GOOD" "$api" "$worker" "$version"
fi

log "ShipIt is up: $url"
log "Dashboard: $(tf_out "$TF_APP" dashboard_url)"
log "Stop it when you are done (~\$0.10/hour):  bash scripts/down.sh"
