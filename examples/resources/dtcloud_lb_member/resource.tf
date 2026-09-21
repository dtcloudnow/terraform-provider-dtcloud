# The machines on the pool's network that can be added as members.
data "dtcloud_lb_vms" "candidates" {
  network_id = dtcloud_network.app.id
}

# One resource per member: each is created and deleted on its own.
resource "dtcloud_lb_member" "web" {
  for_each = { for vm in data.dtcloud_lb_vms.candidates.vms : vm.name => vm }

  lb_id   = dtcloud_lb.public.id
  pool_id = dtcloud_lb_pool.web.pool_id

  name              = each.key
  compute_server_id = each.value.id
  address           = each.value.ip_address
  protocol_port     = 8080
  weight            = 1
}
