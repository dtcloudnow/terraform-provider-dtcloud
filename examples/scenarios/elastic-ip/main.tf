terraform {
  required_providers {
    dtcloud = {
      source = "dtcloudnow/dtcloud"
    }
  }
}

# Credentials, region and endpoint come from the environment. `api_endpoint` is
# required: the provider will not guess which environment to build in.
#   export DTCLOUD_ACCESS_KEY=...
#   export DTCLOUD_SECRET_KEY=...
#   export DTCLOUD_API_URL=<your DT Cloud API base URL, ending in /api/v1>
#   export DTCLOUD_REGION_ID=<region/server id>
#
# Or run `terraform-provider-dtcloud configure` once and drop them all.
provider "dtcloud" {}

variable "external_network_id" {
  type        = string
  description = "External network to allocate addresses from: the one your router's gateway is on. The dtcloud_router data source reports it as external_network_id; `dtctl router externals` prints the same thing."
}

variable "port_id" {
  type        = string
  description = "Port to point the address at, e.g. dtcloud_vm.web.network_interface[0].port_id. Leave empty to allocate without associating."
  default     = ""
}

# An address reserved and pointed at nothing. Perfectly normal: it reports
# status DOWN, and it is yours until you release it. It also occupies quota and
# bills while it sits there.
resource "dtcloud_elastic_ip" "reserved" {
  floating_network_id = var.external_network_id
}

# An address pointed at something. Changing port_id later moves this same
# address; it is never released and reallocated, which is the point — the
# address is the part that is in DNS and in other people's firewall rules.
resource "dtcloud_elastic_ip" "attached" {
  count = var.port_id == "" ? 0 : 1

  floating_network_id = var.external_network_id
  port_id             = var.port_id
}

# Look one up the way a person knows it: by its address.
data "dtcloud_elastic_ip" "reserved" {
  ip_address = dtcloud_elastic_ip.reserved.ip_address
}

# The question this data source is for: what are we paying for that nothing is
# using.
data "dtcloud_elastic_ips" "idle" {
  status = "DOWN"

  depends_on = [dtcloud_elastic_ip.reserved]
}

output "reserved_address" {
  value = dtcloud_elastic_ip.reserved.ip_address
}

output "reserved_network_name" {
  value = data.dtcloud_elastic_ip.reserved.network_name
}

output "idle_addresses" {
  value = data.dtcloud_elastic_ips.idle.ip_addresses
}

output "attached_reaches" {
  value = var.port_id == "" ? "nothing configured" : "${dtcloud_elastic_ip.attached[0].device_owner} ${dtcloud_elastic_ip.attached[0].device_id}"
}
