terraform {
  required_providers {
    dtcloud = {
      source = "dtcloudnow/dtcloud"
    }
  }
}

# Credentials and region are best supplied via environment variables so they
# never end up in committed HCL:
#   export DTCLOUD_ACCESS_KEY=...
#   export DTCLOUD_SECRET_KEY=...
#   export DTCLOUD_API_URL=https://cms.dt.net.tr/api/v1
#   export DTCLOUD_REGION_ID=<region/server id>
provider "dtcloud" {}

resource "dtcloud_ssh_key" "default" {
  name       = "terraform-poc"
  public_key = file("~/.ssh/id_rsa.pub")
}

# Look the same key back up by name.
data "dtcloud_ssh_key" "lookup" {
  name = dtcloud_ssh_key.default.name
}

output "fingerprint" {
  value = data.dtcloud_ssh_key.lookup.fingerprint
}

output "id" {
  value = dtcloud_ssh_key.default.id
}
