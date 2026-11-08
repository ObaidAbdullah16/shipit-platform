# Security design

## Supply chain

| Control | Where |
|---|---|
| Every third-party GitHub Action pinned to a full commit SHA | `.github/` (pinned by the publish script, kept fresh by Dependabot with a 7-day cooldown) |
| Workflows have `permissions: {}` at the top; each job asks only for what it needs | all workflows |
| No long-lived AWS keys: GitHub OIDC → IAM role limited to this repository's `main` branch and `aws` environment | `aws-foundation` |
| Tests, Hadolint, Trivy (image + fixable HIGH/CRITICAL gate), Checkov, gitleaks, Semgrep, zizmor, actionlint on every push | `ci.yml` |
| Images built only in CI, tagged with the commit SHA, **immutable** in ECR | `release.yml`, `infra/bootstrap` |
| Keyless **cosign** signature + **SPDX SBOM attestation** for each image | `.github/actions/build-sign-push` |
| Signature verified against the exact workflow identity before every deploy | `scripts/lib.sh` → `verify_signature` |
| Deploy by digest only (enforced by Terraform validation and `deploy.sh`) | `infra/app/variables.tf`, `scripts/deploy.sh` |

## Runtime

* API image: distroless, static binary, non-root (uid 65532), read-only root filesystem.
* Worker image: slim Python, non-root (uid 10001), read-only root filesystem, `/tmp` on an
  ephemeral volume, container health check.
* Database password generated and stored by RDS in Secrets Manager; ECS injects it at start.
  It never appears in code, Terraform state or logs.
* Security groups: internet → ALB only; ALB → tasks on 8080 only; tasks → RDS on 5432 only.
  Default security group emptied. RDS not publicly accessible, storage encrypted.
* API: URL validation (http/https only, no private/loopback/link-local targets such as
  `169.254.169.254`), request body limit, per-IP rate limit on link creation, security
  headers, request IDs in structured JSON logs.

## Accepted risks (demo budget) and what I would add for a real client

| Skipped (see `.checkov.yaml`) | Production choice |
|---|---|
| Tasks in public subnets with public IPs (no NAT) | Private subnets + NAT or VPC endpoints |
| HTTP only unless a domain is configured | Always HTTPS (ACM is free), HSTS |
| No WAF | AWS WAF managed rules + rate-based rule |
| Redis inside the task | ElastiCache with encryption and AUTH, or SQS |
| Single-AZ RDS, 1-day backups, no deletion protection | Multi-AZ, 7-35 day backups + restore drills (see `restore-drill-kit`), deletion protection |
| 7-day logs, no ALB access logs / VPC flow logs | 90+ day retention, access + flow logs to a log archive account |
| AES256 instead of customer KMS keys | KMS CMKs with key policies per data class |
