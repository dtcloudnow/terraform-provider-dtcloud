---
page_title: "dtcloud: dtcloud_flavors"
subcategory: "Catalogue"
---

# dtcloud_flavors

Lists the compute flavors available for virtual machines.

Use it so a configuration can name a size instead of pasting a uuid, and so the uuid does not
have to be rediscovered when it changes.

## Example Usage

```hcl
data "dtcloud_flavors" "medium" {
  name = "c.medium"
}

resource "dtcloud_vm" "web" {
  name      = "web-01"
  flavor_id = data.dtcloud_flavors.medium.flavors[0].id
  # ...
}
```

Everything on offer:

```hcl
data "dtcloud_flavors" "all" {}

output "sizes" {
  value = [for f in data.dtcloud_flavors.all.flavors : "${f.name}  ${f.vcpus} vCPU  ${f.ram}"]
}
```

## Argument Reference

* `name` - (Optional) Only return flavors whose name matches exactly, case-insensitively.

## Attributes Reference

* `flavors` - The flavors that matched, each with:
  * `id` - use this as a VM's `flavor_id`
  * `name`
  * `vcpus`
  * `ram` - reported as text, e.g. `"4 GB"`, not a number

## Behaviour worth knowing

-> **`name` filters in the provider, not the API.** The endpoint takes no parameters, so the
full catalogue is fetched either way. The filter is there for convenience, not to save a call.

-> **Read-only.** Flavors are defined by the operator; there is no resource here and no
endpoint that would support one.
