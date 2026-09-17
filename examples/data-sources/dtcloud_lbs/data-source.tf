data "dtcloud_lbs" "all" {}

output "load_balancers" {
  value = [for lb in data.dtcloud_lbs.all.load_balancers : "${lb.name}  ${lb.ip_address}  ${lb.status}"]
}
