resource "dtcloud_vm" "web" {
  name      = "web-01"
  flavor_id = data.dtcloud_flavors.small.flavors[0].id
  state     = "running"

  key_name  = dtcloud_ssh_key.deploy.name
  # Written as it should reach the guest; the provider base64-encodes it, which is
  # the only form the API accepts. Encoding it here too would double-encode it.
  user_data = file("${path.module}/cloud-init.yaml")

  # Interfaces declared here are created with the machine and live and die with
  # it. Add one afterwards with dtcloud_vm_network_interface instead.
  network {
    uuid            = dtcloud_network.app.id
    security_groups = [dtcloud_security_group.web.id]

    fixed_ip {
      ip_version = 4
    }
  }

  # The boot disk is boot_index 0.
  block_device {
    boot_index            = 0
    volume_size           = 40
    source_type           = "image"
    device_type           = "disk"
    destination_type      = "volume"
    uuid                  = var.image_id
    volume_type           = "standard"
    delete_on_termination = true
  }
}
