data "dtcloud_projects" "all" {}

# The full quota table. The platform can add keys without warning, so
# they arrive as a map: index it by name.
data "dtcloud_project_limits" "current" {
  project_id = data.dtcloud_projects.all.active_project_id
}

output "core_limit" {
  value = data.dtcloud_project_limits.current.quotas["cores"]
}
