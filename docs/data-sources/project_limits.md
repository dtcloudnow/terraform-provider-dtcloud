---
page_title: "dtcloud: dtcloud_project_limits"
subcategory: "Account"
---

# dtcloud_project_limits

Reports every quota the platform tracks for a project.

This is the full table — around fifty entries covering compute, network, volume and VPN limits.
It is the counterpart to [`dtcloud_project_quotas`](project_quotas.md), which reports a handful
of headline figures **with current usage**. This one reports allowances only.

## Example Usage

```hcl
data "dtcloud_projects" "all" {}

data "dtcloud_project_limits" "current" {
  project_id = data.dtcloud_projects.all.active_project_id
}

output "instances" {
  value = data.dtcloud_project_limits.current.quotas["instances"]
}

output "everything_tracked" {
  value = data.dtcloud_project_limits.current.names
}
```

## Argument Reference

* `project_id` - (Required) ID of the project, from `dtcloud_projects`.

## Attributes Reference

* `quotas` - a map of every quota, keyed by its OpenStack name.
* `names` - the quota names, sorted. Useful for discovering what the platform tracks.

## Behaviour worth knowing

~> **The values are text, including the numbers.** A Terraform map holds one type, and the API
mixes numbers with the word `Unlimited` (its `-1`). Everything is therefore rendered as text —
use `tonumber()` when you need arithmetic, and check for `"Unlimited"` first:

```hcl
locals {
  instance_limit = data.dtcloud_project_limits.current.quotas["instances"]
  can_build_more = local.instance_limit == "Unlimited" || tonumber(local.instance_limit) > 10
}
```

-> **The keys are OpenStack's own** — `cores`, `instances`, `security_group_rules`, `router`,
`vpnservice` and so on — and the platform can add more without warning. That is why this is a
map rather than a fixed set of attributes: a new quota appears on its own, without a provider
release.
