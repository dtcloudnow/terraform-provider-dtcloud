---
page_title: "dtcloud: dtcloud_project_quotas"
subcategory: "Account"
---

# dtcloud_project_quotas

Reports what a project is using against what it is allowed.

This is the console's summary: a few headline figures with usage and quota side by side. For
the full quota table — every limit the platform tracks — use
[`dtcloud_project_limits`](project_limits.md), which reports allowances without usage.

## Example Usage

```hcl
data "dtcloud_projects" "all" {}

data "dtcloud_project_quotas" "current" {
  project_id = data.dtcloud_projects.all.active_project_id
}

output "cpu" {
  value = "${data.dtcloud_project_quotas.current.cpu[0].usage} / ${data.dtcloud_project_quotas.current.cpu[0].quota}"
}

output "vms" {
  value = data.dtcloud_project_quotas.current.vm_status[0]
}
```

## Argument Reference

* `project_id` - (Required) ID of the project, from `dtcloud_projects`.
* `region_id` - (Optional) Region to report on. Defaults to the provider's configured region.

## Attributes Reference

* `cpu` / `ram` / `floating_ips` - each a single block with `usage` and `quota`.
  `ram` is in GiB.
* `storage_space` - one block per volume type, with `name`, `usage` and `quota`.
* `vm_status` - a single block counting the project's VMs: `count`, `running`, `stopped`,
  `error` and `in_progress`.

## Behaviour worth knowing

~> **A `quota` of `-1` means unlimited.** It is passed through as `-1` rather than translated,
so that arithmetic on it stays honest — check for it before dividing.

-> **`in_progress` counts VMs with a pending task**, whatever their nominal status. A VM that is
mid-resize appears there rather than under `running` or `stopped`.

-> **This endpoint takes the region in its path**, unlike every other call in this provider,
which passes it as a query parameter. That is why `region_id` exists here as an argument; it
falls back to the provider's setting.

-> The API also returns a `topVms` ranking assembled from metrics. It is not exposed: its shape
is not pinned down, and a value that moves on every read has no place in a resource argument.
