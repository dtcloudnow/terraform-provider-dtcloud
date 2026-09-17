# Lookup is by id: load balancer names are not unique.
data "dtcloud_lb" "existing" {
  id = "3d372446-4c75-4327-9511-98442456162f"
}

# The VIP sits on a network block rather than a top-level field.
output "vip" {
  value = data.dtcloud_lb.existing.network[0].ip
}
