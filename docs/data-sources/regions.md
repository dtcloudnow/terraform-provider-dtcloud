---
page_title: "dtcloud: dtcloud_regions"
subcategory: "Account"
---

# dtcloud_regions

Lists the regions available to the account, and which services each one offers.

## Example Usage

```hcl
data "dtcloud_regions" "all" {}

output "regions" {
  value = [for r in data.dtcloud_regions.all.regions : "${r.id}  ${r.name}"]
}
```

Checking a region supports what you are about to build:

```hcl
locals {
  here = one([
    for r in data.dtcloud_regions.all.regions : r if r.id == 1
  ])
}

resource "dtcloud_network" "app" {
  count = contains(local.here.available_services, "networks") ? 1 : 0

  name = "app-net"
  cidr = "10.20.30.0/24"
}
```

## Attributes Reference

* `regions` - The regions visible to the caller, each with:
  * `id` - the numeric id the provider's `region_id` argument takes
  * `name`
  * `available_services` - the services this region offers, sorted
* `ids` - just the ids.

## Behaviour worth knowing

-> **`available_services` lists only what is available.** A service the region reports as
unavailable is left out rather than listed as false, so `contains()` is the natural check. A
region the platform reports no availability information for comes back with an empty list, not
a missing entry.

-> **Changing the active region is deliberately not exposed.** The API has an endpoint for it,
but it switches the *caller's session*, not infrastructure — every subsequent call would land
somewhere else. The provider takes its region through `region_id` instead.
