terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = "us-west-2"
}

data "aws_region" "current" {}

# Pulls VPC, ALB, ECS cluster, IAM, SNS topic, and ECR refs from the
# shared environment/ Terraform state.
data "terraform_remote_state" "environment" {
  backend = "s3"
  config = {
    bucket = var.backend_bucket
    key    = "${var.environment}/environment.tfstate"
    region = "us-west-2"
  }
}
