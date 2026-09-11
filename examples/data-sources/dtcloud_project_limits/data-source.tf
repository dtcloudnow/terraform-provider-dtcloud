# The full OpenStack quota table. The platform can add keys without warning, so
# they arrive as a map: index it by name.
data "dtcloud_project_limits" "mine" {}

output "core_limit" {
  value = data.dtcloud_project_limits.mine.quotas["cores"]
}
