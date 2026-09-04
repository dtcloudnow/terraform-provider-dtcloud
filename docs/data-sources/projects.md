---
page_title: "dtcloud: dtcloud_projects"
subcategory: "Account"
---

# dtcloud_projects

Lists the projects on the account, and reports which one the current session is pointed at.

## Example Usage

```hcl
data "dtcloud_projects" "all" {}

output "working_in" {
  value = data.dtcloud_projects.all.active_project_name
}
```

Feeding the active project into the quota data sources:

```hcl
data "dtcloud_project_quotas" "current" {
  project_id = data.dtcloud_projects.all.active_project_id
}
```

## Attributes Reference

* `projects` - The projects visible to the caller, each with `id`, `name` and `domain_id`.
* `active_project_id` - the project the session is pointed at.
* `active_project_name` - its name.
* `active_region_id` - the region the session is pointed at.

## Behaviour worth knowing

~> **`active_project_id` is where everything lands.** Every resource this provider creates goes
into that project. It is worth outputting it in any configuration that manages more than a
handful of resources, so a misconfigured credential is obvious rather than surprising.

-> **Changing the active project is deliberately not exposed.** The API has an endpoint for it,
but it switches the caller's session rather than any piece of infrastructure — driving it from
a resource would silently move where every other call lands.
