#!/usr/bin/env bash
# Roll out a release to the running ShipIt service.
#   bash scripts/deploy.sh <api image@sha256:...> <worker image@sha256:...> <version> [--chaos]
#
# 1. verifies both images were signed by this repo's release workflow (cosign)
# 2. registers a new task definition revision with the new images (by digest)
# 3. updates the ECS service and follows the rolling deployment
# 4. if the new tasks fail their health checks, the ECS circuit breaker rolls back
#    to the previous revision automatically -> this script exits 1
# 5. on success, smoke-tests the live URL and records the release as "last good"
#
# --chaos starts the API with CHAOS_FAIL_HEALTH=1 (its /healthz returns 500) to
# demonstrate the automatic rollback. scripts/break-it.sh wraps this.
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need aws terraform jq curl

[[ $# -ge 3 ]] || die "usage: deploy.sh <api image@digest> <worker image@digest> <version> [--chaos]"
api="$1"; worker="$2"; version="$3"; chaos=0
[[ "${4:-}" == "--chaos" ]] && chaos=1
for img in "$api" "$worker"; do
  [[ "$img" =~ @sha256:[0-9a-f]{64}$ ]] || die "Deploy by digest only (got $img)"
done

app_is_up || die "ShipIt is not running. Start it with: bash scripts/up.sh"
cluster="$(tf_out "$TF_APP" cluster_name)"; service="$(tf_out "$TF_APP" service_name)"; url="$(tf_out "$TF_APP" url)"

verify_signature "$api"
verify_signature "$worker"

work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
current="$(aws ecs describe-services --cluster "$cluster" --services "$service" --query 'services[0].taskDefinition' --output text)"
aws ecs describe-task-definition --task-definition "$current" --query taskDefinition --output json > "$work/current.json"

jq --arg api "$api" --arg worker "$worker" --arg chaos "$chaos" '
  {family, taskRoleArn, executionRoleArn, networkMode, containerDefinitions, volumes,
   requiresCompatibilities, cpu, memory, runtimePlatform}
  | .containerDefinitions |= map(
      if .name == "api" then
        .image = $api
        | .environment = ([.environment[] | select(.name != "CHAOS_FAIL_HEALTH")] + [{name: "CHAOS_FAIL_HEALTH", value: $chaos}])
      elif .name == "worker" then .image = $worker
      else . end)' "$work/current.json" > "$work/new.json"

new_td="$(aws ecs register-task-definition --cli-input-json "file://$work/new.json" \
  --tags "key=Version,value=$version" "key=Chaos,value=$chaos" \
  --query taskDefinition.taskDefinitionArn --output text)"
log "Registered ${new_td##*/} (version $version$([[ $chaos == 1 ]] && echo ', CHAOS: /healthz will fail'))"

aws ecs update-service --cluster "$cluster" --service "$service" --task-definition "$new_td" >/dev/null
start=$(date +%s)
log "Rolling deployment started (old tasks keep serving until the new ones are healthy)"

last_event=""
while :; do
  sleep 10
  svc="$(aws ecs describe-services --cluster "$cluster" --services "$service" --query 'services[0]' --output json)"
  event="$(jq -r '.events[0].message // ""' <<<"$svc")"
  [[ "$event" != "$last_event" ]] && { log "ECS: $event"; last_event="$event"; }
  state="$(jq -r --arg td "$new_td" '[.deployments[] | select(.taskDefinition == $td)][0].rolloutState // "GONE"' <<<"$svc")"
  primary="$(jq -r '[.deployments[] | select(.status == "PRIMARY")][0].taskDefinition' <<<"$svc")"
  elapsed=$(( $(date +%s) - start ))
  if [[ "$state" == "COMPLETED" && "$primary" == "$new_td" ]]; then
    log "Deployment completed in ${elapsed}s"
    break
  fi
  if [[ "$state" == "FAILED" || "$primary" != "$new_td" ]]; then
    reason="$(jq -r --arg td "$new_td" '[.deployments[] | select(.taskDefinition == $td)][0].rolloutStateReason // "circuit breaker triggered"' <<<"$svc")"
    warn "Deployment FAILED after ${elapsed}s: $reason"
    warn "ECS rolled back automatically to ${primary##*/} - users kept being served by the previous release."
    exit 1
  fi
  (( elapsed < 1200 )) || die "Deployment still in progress after 20 minutes - check: bash scripts/status.sh"
done

if [[ "$chaos" == "1" ]]; then
  warn "Chaos release went live without being rolled back (unexpected) - run: bash scripts/rollback.sh"
  exit 1
fi
smoke_test "$url" "$version" || die "Smoke test failed after deploy - run: bash scripts/rollback.sh"
put_release "$SSM_LAST_GOOD" "$api" "$worker" "$version"
log "Release $version is live at $url"
