terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
    archive = {
      source = "hashicorp/archive"
    }
    null = {
      source = "hashicorp/null"
    }
  }

  backend "s3" {
    bucket = "registry-tfstate"
    key    = "terraform.tfstate"
    dynamodb_table = "terraform_locks"

    region = "eu-west-1"
  }

  required_version = "1.6.0-alpha1"
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project = "registry"
    }
  }
}

provider "aws" {
  region = "us-east-1"

  alias = "us-east-1"

  default_tags {
    tags = {
      Project = "registry"
    }
  }
}