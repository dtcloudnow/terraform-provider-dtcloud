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

variable "cidr" {
  type        = string
  description = "IPv4 range for the subnet."
  default     = "10.99.0.0/24"
}

# A network with address management on: the platform creates a subnet from the
# CIDR and enables port security.
resource "dtcloud_network" "example" {
  name        = "terraform-network-example"
  cidr        = var.cidr
  enable_dhcp = true

  dns_nameservers = ["8.8.8.8", "1.1.1.1"]

  # Leave allocation_pools out to let the platform choose the range; it is set
  # here to show the shape.
  allocation_pools {
    start = cidrhost(var.cidr, 10)
    end   = cidrhost(var.cidr, 200)
  }
}

# The other half of the resource: no address management, so no subnet at all.
# ipam_enabled also turns port security off on the network.
resource "dtcloud_network" "isolated" {
  name         = "terraform-network-isolated"
  ipam_enabled = false
}

# Read the managed network back.
data "dtcloud_network" "example" {
  id = dtcloud_network.example.id
}

# Every virtual network in the region. Both filters are applied by the API.
data "dtcloud_networks" "virtual" {
  network_type = "Virtual"

  depends_on = [dtcloud_network.example]
}

output "id" {
  value = dtcloud_network.example.id
}

output "subnet_id" {
  value = dtcloud_network.example.subnet_id
}

# Filled in by the platform when gateway_ip is omitted.
output "gateway" {
  value = data.dtcloud_network.example.gateway_ip
}

output "allocation_pools" {
  value = [for p in data.dtcloud_network.example.allocation_pools : "${p.start} - ${p.end}"]
}

# The isolated network has no subnet, so this is empty by design.
output "isolated_subnet_id" {
  value = dtcloud_network.isolated.subnet_id
}

output "virtual_network_count" {
  value = length(data.dtcloud_networks.virtual.networks)
}
