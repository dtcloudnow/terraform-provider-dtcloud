terraform {
  required_providers {
    dtcloud = {
      source  = "dtcloudnow/dtcloud"
      version = "~> 0.1"
    }
  }
}

# Credentials are prompted for by Terraform: a variable with no default is asked
# for on every run that does not already have it. Put the values in a gitignored
# terraform.tfvars, or export DTCLOUD_ACCESS_KEY / DTCLOUD_SECRET_KEY instead.
variable "dtcloud_access_key" {
  type = string
}

variable "dtcloud_secret_key" {
  type      = string
  sensitive = true
}

variable "dtcloud_region_id" {
  type = string
}

provider "dtcloud" {
  access_key = var.dtcloud_access_key
  secret_key = var.dtcloud_secret_key
  region_id  = var.dtcloud_region_id
}
