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

# There is no way to ask the platform which network is the external one: the
# network list carries no such flag, and a region can hold several physical
# networks of which only one is routable. So this has to be given, not guessed.
# An existing router names it: `dtcloud_routers` reports it as external_network.
variable "external_network_id" {
  type        = string
  description = "ID of the external network the router's gateway sits on. Required; ask your operator or read it off an existing router."
}

variable "cidr" {
  type        = string
  description = "IPv4 range for the private network behind the router."
  default     = "10.98.0.0/24"
}

# A private network for the machines that will sit behind the router.
resource "dtcloud_network" "app" {
  name = "terraform-router-example-app"
  cidr = var.cidr
}

# Every router has an external gateway; there is no way to create one without.
# It costs one public address from the project quota.
resource "dtcloud_router" "example" {
  name                = "terraform-router-example"
  external_network_id = var.external_network_id
  enable_snat         = true
}

# Give the private network a way out.
resource "dtcloud_router_interface" "app" {
  router_id  = dtcloud_router.example.id
  network_id = dtcloud_network.app.id

  # Leave ip_address out and the platform attaches the network's first subnet
  # and picks the address itself — the gateway address, in practice. Set it to
  # ask for a particular one instead.
}

# Traffic for another network goes to a next hop reachable through the
# interface above, rather than out of the gateway.
resource "dtcloud_router_static_route" "branch" {
  router_id   = dtcloud_router.example.id
  destination = "192.168.50.0/24"
  next_hop    = cidrhost(var.cidr, 20)

  # Not decoration. A static route whose next hop sits on an attached network
  # pins that interface: the platform refuses to detach it while the route
  # exists. Without this, a destroy can try the interface first and fail.
  depends_on = [dtcloud_router_interface.app]
}

# Read the router back through the details endpoint, which reports the external
# network by id.
data "dtcloud_router" "example" {
  id = dtcloud_router.example.id
}

# The listing endpoint reports the same gateway by name instead.
data "dtcloud_routers" "all" {
  depends_on = [dtcloud_router.example]
}

data "dtcloud_router_interfaces" "example" {
  router_id  = dtcloud_router.example.id
  depends_on = [dtcloud_router_interface.app]
}

data "dtcloud_router_static_routes" "example" {
  router_id  = dtcloud_router.example.id
  depends_on = [dtcloud_router_static_route.branch]
}

output "id" {
  value = dtcloud_router.example.id
}

# Assigned by the platform; there is no way to ask for a particular one.
output "gateway_address" {
  value = dtcloud_router.example.external_fixed_ip[0].ip_address
}

# The name of the same external network the resource reports by id.
output "external_network_name" {
  value = one([for r in data.dtcloud_routers.all.routers : r.external_network if r.id == dtcloud_router.example.id])
}

output "interface_port_id" {
  value = dtcloud_router_interface.app.port_id
}

output "routes" {
  value = {
    for r in data.dtcloud_router_static_routes.example.routes :
    r.destination => r.next_hop
  }
}
