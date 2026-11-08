#!/usr/bin/env bash
# Show whether ShipIt is running, which release is live and what it costs.
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need aws terraform jq

if app_is_up; then
  echo "shipit: UP (~\$0.10/hour) - $(tf_out "$TF_APP" url)   stop: bash scripts/down.sh"
else
  echo "shipit: DOWN (costs nothing per hour; signed images kept in ECR: cents/month - 'down --all' removes them)"
fi
show_release() {
  local j; j="$(release_json "$1")"
  if [[ -n "$j" ]]; then jq -r '"\(.version) (\(.time))"' <<<"$j"; else echo none; fi
}
echo "Latest signed release : $(show_release "$SSM_LATEST")"
echo "Last good in AWS      : $(show_release "$SSM_LAST_GOOD")"

if ! app_is_up; then
  exit 0
fi
cluster="$(tf_out "$TF_APP" cluster_name)"; service="$(tf_out "$TF_APP" service_name)"; url="$(tf_out "$TF_APP" url)"
svc="$(aws ecs describe-services --cluster "$cluster" --services "$service" --query 'services[0]' --output json)"
echo "App stack             : UP (~\$0.10/hour - stop with: bash scripts/down.sh)"
echo "URL                   : $url"
echo "Tasks running/desired : $(jq -r '"\(.runningCount)/\(.desiredCount)"' <<<"$svc")"
echo "Task definition       : $(jq -r '.taskDefinition | split("/")[1]' <<<"$svc")"
echo "Live version          : $(curl -fsS -m 5 "$url/version" 2>/dev/null | jq -r .version 2>/dev/null || echo unreachable)"
echo "Deployments:"
jq -r '.deployments[] | "  \(.status) \(.taskDefinition | split("/")[1]) rollout=\(.rolloutState) running=\(.runningCount)"' <<<"$svc"
echo "Recent events:"
jq -r '.events[:5][] | "  \(.createdAt) \(.message)"' <<<"$svc"
echo "Alarms in ALARM state:"
aws cloudwatch describe-alarms --alarm-name-prefix shipit- --state-value ALARM --query 'MetricAlarms[].AlarmName' --output text | tr '\t' '\n' | sed 's/^/  /'
