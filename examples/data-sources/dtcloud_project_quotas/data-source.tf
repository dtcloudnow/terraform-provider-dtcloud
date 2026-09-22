data "dtcloud_projects" "all" {}

# Usage against allowance, as the console shows it.
data "dtcloud_project_quotas" "current" {
  project_id = data.dtcloud_projects.all.active_project_id
}

output "cpu" {
  value = "${data.dtcloud_project_quotas.current.cpu[0].usage} of ${data.dtcloud_project_quotas.current.cpu[0].quota}"
}
