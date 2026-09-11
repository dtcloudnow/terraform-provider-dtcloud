data "dtcloud_projects" "all" {}

output "active_project" {
  value = data.dtcloud_projects.all.active_project_name
}
