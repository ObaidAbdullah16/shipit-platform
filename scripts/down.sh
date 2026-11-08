#!/usr/bin/env bash
# Stop ShipIt: destroy everything that costs money (load balancer, Fargate, RDS,
# VPC, alarms, dashboard). The ECR images and release records are kept (cents per
# month) so `up` can start the same signed release again.
#   bash scripts/down.sh          # normal stop
#   bash scripts/down.sh --all    # also delete the ECR repositories and release records
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need aws terraform jq

if app_is_up; then
  log "Terraform destroy (takes ~5-10 minutes)"
  tf_destroy "$TF_APP" -var "api_image=$DUMMY_IMAGE" -var "worker_image=$DUMMY_IMAGE"
else
  log "App stack is already down"
fi

# Task definition revisions registered by deploy.sh are not in Terraform state.
for td in $(aws ecs list-task-definitions --family-prefix shipit --status ACTIVE --query 'taskDefinitionArns[]' --output text 2>/dev/null); do
  [[ "$td" == *":task-definition/shipit:"* ]] || continue
  aws ecs deregister-task-definition --task-definition "$td" >/dev/null && log "Deregistered ${td##*/}"
done

if [[ "${1:-}" == "--all" ]]; then
  log "Deleting the ECR repositories (all images) and release records"
  tf_init "$TF_BOOT" "shipit-platform/bootstrap.tfstate"
  tf_destroy "$TF_BOOT"
  aws ssm delete-parameters --names "$SSM_LATEST" "$SSM_LAST_GOOD" >/dev/null 2>&1 || true
fi
log "Down. Nothing from ShipIt is billing by the hour any more."
