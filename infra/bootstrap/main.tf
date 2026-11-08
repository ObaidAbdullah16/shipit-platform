# Long-lived, almost-free part of ShipIt: the two container registries.
# Created once (scripts/bootstrap.sh) and kept when the app is stopped, so
# signed images and their history survive `down`. Cost: a few cents per month.
terraform {
  required_version = ">= 1.10.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
  backend "s3" {}
}

variable "aws_region" {
  type    = string
  default = "ap-southeast-2"
}

variable "keep_images" {
  description = "How many images to keep per repository (older ones are deleted automatically)."
  type        = number
  default     = 15
}

provider "aws" {
  region = var.aws_region
  default_tags {
    tags = {
      Project   = "shipit-platform"
      ManagedBy = "terraform"
    }
  }
}

locals {
  repos = ["shipit/api", "shipit/worker"]
}

resource "aws_ecr_repository" "this" {
  for_each = toset(local.repos)
  name     = each.value
  # A tag (the git commit SHA) can never be overwritten, and releases are deployed
  # by digest anyway. cosign signatures and SBOM attestations are stored next to them.
  image_tag_mutability = "IMMUTABLE"
  force_delete         = true

  image_scanning_configuration {
    scan_on_push = true
  }
  encryption_configuration {
    encryption_type = "AES256"
  }
}

resource "aws_ecr_lifecycle_policy" "this" {
  for_each   = aws_ecr_repository.this
  repository = each.value.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep the newest ${var.keep_images} images (signatures and SBOM attestations count too)"
      selection = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = var.keep_images * 3
      }
      action = { type = "expire" }
    }]
  })
}

output "api_repository_url" {
  value = aws_ecr_repository.this["shipit/api"].repository_url
}

output "worker_repository_url" {
  value = aws_ecr_repository.this["shipit/worker"].repository_url
}

output "registry" {
  value = split("/", aws_ecr_repository.this["shipit/api"].repository_url)[0]
}
