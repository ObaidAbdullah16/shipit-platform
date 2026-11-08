# Costs

Prices: `ap-south-1` (Mumbai) on-demand, rounded, October 2026. The demo account runs in `ap-southeast-2`
(Sydney), which is about 20-30% dearer (~$0.10/hour while up). Check the
[AWS pricing calculator](https://calculator.aws/) for your region.

## While running (`up`)

| Resource | ≈ per hour |
|---|---|
| Application Load Balancer (incl. ~1 LCU) | $0.03 |
| RDS PostgreSQL `db.t4g.micro` + 20 GB gp3 | $0.024 |
| Fargate ARM64 0.25 vCPU / 0.5 GB | $0.01 |
| 3 public IPv4 addresses (ALB ×2, task ×1) | $0.015 |
| Secrets Manager, CloudWatch, data transfer | < $0.005 |
| **Total** | **≈ $0.08/hour** (≈ $1.90 if left on for a day) |

A 2-hour demo session costs about **$0.16**. Recording all demo videos takes one session.

## While stopped (`down`)

| Resource | ≈ per month |
|---|---|
| ECR images (≈ 15 releases × 2 images, ~60 MB each, ~1-2 GB) | $0.10-0.20 |
| SSM parameters, Terraform state | $0.00 |

## Guard rails

* `scripts/down.sh` deletes everything billed by the hour.
* `auto-stop.yml` runs `down` every night at 00:00 IST.
* `aws-foundation` budgets: $1/day and $10/month alerts by email.
