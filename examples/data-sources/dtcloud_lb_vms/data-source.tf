# The machines on a network that can be added to a pool. This is the lookup the
# web console offers when you pick members, and it is what makes a `for_each`
# member set possible.
data "dtcloud_lb_vms" "candidates" {
  network_id = dtcloud_network.app.id
}

output "candidates" {
  # A machine can hold several addresses on one network, so `ip_addresses` is a list.
  value = [for vm in data.dtcloud_lb_vms.candidates.vms : "${vm.name}  ${join(",", vm.ip_addresses)}"]
}
