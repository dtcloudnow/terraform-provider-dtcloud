terraform {
  required_providers {
    dtcloud = {
      source = "dtcloudnow/dtcloud"
    }
  }
}

# Credentials and region come from the environment:
#   export DTCLOUD_ACCESS_KEY=...
#   export DTCLOUD_SECRET_KEY=...
#   export DTCLOUD_REGION_ID=1
provider "dtcloud" {}

# Everything here is read-only. Running `apply` on this creates nothing —
# these are the catalogue and account data sources you use to look up the ids
# and limits that the other services need.

data "dtcloud_regions" "all" {}

data "dtcloud_projects" "all" {}

data "dtcloud_flavors" "all" {}

data "dtcloud_project_quotas" "current" {
  project_id = data.dtcloud_projects.all.active_project_id
}

data "dtcloud_project_limits" "current" {
  project_id = data.dtcloud_projects.all.active_project_id
}

output "regions" {
  value = [for r in data.dtcloud_regions.all.regions : "${r.id}  ${r.name}"]
}

# Which services a region offers — a configuration that builds something the
# region does not support fails at apply time, so check first.
output "services_here" {
  value = one([
    for r in data.dtcloud_regions.all.regions :
    r.available_services if r.id == tonumber(data.dtcloud_projects.all.active_region_id)
  ])
}

output "active_project" {
  value = "${data.dtcloud_projects.all.active_project_name} (${data.dtcloud_projects.all.active_project_id})"
}

output "flavors" {
  value = [for f in data.dtcloud_flavors.all.flavors : "${f.name}  ${f.vcpus} vCPU  ${f.ram}"]
}

# Usage against allowance. -1 means unlimited.
output "usage" {
  value = {
    cpu          = "${data.dtcloud_project_quotas.current.cpu[0].usage} / ${data.dtcloud_project_quotas.current.cpu[0].quota}"
    ram_gib      = "${data.dtcloud_project_quotas.current.ram[0].usage} / ${data.dtcloud_project_quotas.current.ram[0].quota}"
    floating_ips = "${data.dtcloud_project_quotas.current.floating_ips[0].usage} / ${data.dtcloud_project_quotas.current.floating_ips[0].quota}"
  }
}

output "vms_by_state" {
  value = data.dtcloud_project_quotas.current.vm_status[0]
}

output "storage_usage" {
  value = [
    for s in data.dtcloud_project_quotas.current.storage_space :
    "${s.name}: ${s.usage} / ${s.quota}"
  ]
}

# The full OpenStack quota table. Index it by name.
output "instance_limit" {
  value = data.dtcloud_project_limits.current.quotas["instances"]
}

output "tracked_limits" {
  value = length(data.dtcloud_project_limits.current.names)
}
