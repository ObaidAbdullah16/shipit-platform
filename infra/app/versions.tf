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

provider "aws" {
  region = var.aws_region
  default_tags {
    tags = {
      Project   = "shipit-platform"
      ManagedBy = "terraform"
      AutoStop  = "true"
    }
  }
}

data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}
data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  name       = var.name
  azs        = slice(data.aws_availability_zones.available.names, 0, 2)
  https      = var.domain_name != "" && var.hosted_zone_id != ""
  public_url = local.https ? "https://${var.domain_name}" : "http://${aws_lb.this.dns_name}"
}
