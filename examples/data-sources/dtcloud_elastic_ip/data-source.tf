# By address -- the half that is actually useful, since an address is what is
# written down in DNS or in somebody's firewall rule.
data "dtcloud_elastic_ip" "web" {
  ip_address = "203.0.113.42"
}

data "dtcloud_elastic_ip" "by_id" {
  id = var.elastic_ip_id
}
