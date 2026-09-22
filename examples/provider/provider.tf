terraform {
  required_providers {
    dtcloud = {
      source  = "dtcloudnow/dtcloud"
      version = "~> 26.0.0"
    }
  }
}

# Terraform asks for every variable that has no default and no value yet. Put the
# values in a terraform.tfvars you do not commit, or use the DTCLOUD_* environment
# variables instead. The endpoint defaults to production; set it for any other
# environment.
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

variable "dtcloud_api_url" {
  type    = string
  default = "https://console.dt.net.tr/api/v1"
}

provider "dtcloud" {
  access_key   = var.dtcloud_access_key
  secret_key   = var.dtcloud_secret_key
  region_id    = var.dtcloud_region_id
  api_endpoint = var.dtcloud_api_url
}
