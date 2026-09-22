# Allocate a public address and leave it unattached.
resource "dtcloud_elastic_ip" "spare" {
  floating_network_id = var.external_network_id
}

# Allocate and point it at a machine's port. Moving the address to another port
# is an in-place update: the address itself is never released.
resource "dtcloud_elastic_ip" "web" {
  floating_network_id = var.external_network_id
  port_id             = dtcloud_vm_network_interface.web.port_id
}
