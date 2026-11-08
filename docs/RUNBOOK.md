# Runbook

Everything works from the **Actions tab → Control (start / stop) → Run workflow**, or from a
terminal (WSL) in the repository folder.

| Task | Actions tab (`action`) | Terminal |
|---|---|---|
| Start (~10-12 min) | `up` | `bash scripts/up.sh` |
| Stop (~5-10 min) | `down` | `bash scripts/down.sh` |
| What is running / costing? | `status` | `bash scripts/status.sh` |
| Demo a bad release + automatic rollback | `break-it` | `bash scripts/break-it.sh` |
| Roll back to the last good release | `rollback` | `bash scripts/rollback.sh` |
| Generate traffic for the dashboard | `loadtest` | `bash scripts/loadtest.sh 120 8` |
| Delete everything incl. images | `down-all` | `bash scripts/down.sh --all` |

A nightly workflow (`auto-stop.yml`, 00:00 IST) runs `down` in case you forget.

## Alarms

| Alarm | Meaning | First steps |
|---|---|---|
| `shipit-unhealthy-targets` | A task fails `/healthz` | `status.sh` → look at *Recent events*; a failing deployment is rolled back automatically. If the alarm stays, `rollback.sh`. |
| `shipit-api-5xx` | >5 server errors per minute | Logs: CloudWatch → `/ecs/shipit` → filter `"level":"ERROR"`. Check `/readyz` (Postgres/Redis). |
| `shipit-p95-latency` | p95 > 500 ms for 3 min | Dashboard: is RDS CPU high? Is traffic unusual (load test)? Scale `desired_count` or task size. |
| `shipit-db-cpu` | RDS CPU > 80% for 5 min | Look for a traffic spike; the `t4g.micro` instance has burst credits that can run out. |

If `incidentscribe-ai` is deployed, each alarm also produces an AI-written incident summary
(Slack or its demo page) with matching runbook links.

## Useful commands

```bash
# Follow logs of all three containers
aws logs tail /ecs/shipit --follow

# Last 10 service events (deployments, health check failures, rollbacks)
aws ecs describe-services --cluster shipit --services shipit \
  --query 'services[0].events[:10].[createdAt,message]' --output table

# Who signed the running image?
cosign verify <image@sha256:...> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp 'https://github.com/.*/shipit-platform/.*'

# Download the SBOM attestation of an image
cosign verify-attestation --type spdxjson <image@sha256:...> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp 'https://github.com/.*/shipit-platform/.*' \
  | jq -r .payload | base64 -d | jq '.predicate.packages | length'
```

## Postmortem template

```
Title / date / duration / severity
Impact:      who noticed what, for how long
Timeline:    detection -> mitigation -> resolution (UTC)
Root cause:  the technical cause and why it was possible
What went well / what did not
Action items: owner, due date (prevent, detect faster, mitigate faster)
```
