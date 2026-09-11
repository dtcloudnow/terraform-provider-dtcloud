# A network with IPAM on: the provider also manages the subnet inside it.
resource "dtcloud_network" "app" {
  name         = "app-net"
  ipam_enabled = true
  cidr         = "10.20.0.0/24"

  gateway_ip      = "10.20.0.1"
  enable_dhcp     = true
  dns_nameservers = ["8.8.8.8", "8.8.4.4"]
}

# With IPAM off there is no subnet, and addresses are managed outside Terraform.
resource "dtcloud_network" "flat" {
  name         = "flat-net"
  ipam_enabled = false
}
