# Each size is offered twice: once with high availability and once without. That
# flag is how HA is chosen -- there is no separate switch on the load balancer.
data "dtcloud_lb_flavors" "small_ha" {
  name = "small"
  ha   = true
}

output "flavor_id" {
  value = data.dtcloud_lb_flavors.small_ha.flavors[0].id
}
