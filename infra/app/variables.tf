variable "aws_region" {
  type    = string
  default = "ap-southeast-2"
}

variable "name" {
  description = "Prefix for every resource (IAM role names must start with \"shipit\" - see aws-foundation)."
  type        = string
  default     = "shipit"
}

variable "api_image" {
  description = "API image by digest, e.g. 123456789012.dkr.ecr.ap-southeast-2.amazonaws.com/shipit/api@sha256:... (scripts/up.sh fills this in)."
  type        = string
  validation {
    condition     = can(regex("@sha256:[0-9a-f]{64}$", var.api_image))
    error_message = "Deploy by digest (…@sha256:…), never by a mutable tag."
  }
}

variable "worker_image" {
  description = "Worker image by digest."
  type        = string
  validation {
    condition     = can(regex("@sha256:[0-9a-f]{64}$", var.worker_image))
    error_message = "Deploy by digest (…@sha256:…), never by a mutable tag."
  }
}

variable "redis_image" {
  description = "Redis sidecar image (ECR Public mirror of the Docker official image: no Docker Hub rate limits)."
  type        = string
  default     = "public.ecr.aws/docker/library/redis:7-alpine"
}

variable "desired_count" {
  type    = number
  default = 1
}

variable "task_cpu" {
  description = "Fargate CPU units for the whole task (api + worker + redis). 256 = 0.25 vCPU."
  type        = number
  default     = 256
}

variable "task_memory" {
  description = "Fargate memory (MiB) for the whole task."
  type        = number
  default     = 512
}

variable "db_instance_class" {
  type    = string
  default = "db.t4g.micro"
}

variable "log_retention_days" {
  type    = number
  default = 7
}

variable "allowed_cidrs" {
  description = "Who may reach the load balancer. Default: everyone (it is a public demo)."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "domain_name" {
  description = "Optional custom domain (e.g. shipit.example.com). Needs hosted_zone_id too; enables HTTPS with a free ACM certificate."
  type        = string
  default     = ""
}

variable "hosted_zone_id" {
  description = "Optional Route 53 hosted zone ID that contains domain_name."
  type        = string
  default     = ""
}

variable "permissions_boundary_name" {
  description = "IAM permissions boundary that aws-foundation creates. Every role this stack creates carries it; the GitHub deploy role refuses to create roles without it. Set \"\" only when you deploy without aws-foundation."
  type        = string
  default     = "gha-workload-boundary"
}
