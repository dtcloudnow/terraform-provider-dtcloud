data "dtcloud_elastic_ips" "all" {}

# Allocated but attached to nothing: still billed, still using quota.
data "dtcloud_elastic_ips" "idle" {
  status = "DOWN"
}
