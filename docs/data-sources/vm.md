---
page_title: "dtcloud: dtcloud_vm"
subcategory: "Compute"
---

# dtcloud_vm (Data Source)

Get information about an existing virtual machine by ID.

## Example Usage

```hcl
data "dtcloud_vm" "existing" {
  id = "9f1c2b3a-0000-4a1b-8c2d-1234567890ab"
}

output "vm_status" {
  value = data.dtcloud_vm.existing.status
}
```

## Argument Reference

* `id` - (Required) ID of the virtual machine.

-> Lookup is by ID rather than by name because VM names are not unique — the platform allows
several VMs to share a name, and its multi-create path generates `name-1`, `name-2`, … so a
name could not resolve to a single instance.

## Attributes Reference

* `name` - Name of the virtual machine.
* `status` - Current state, e.g. `ACTIVE`, `SHUTOFF`, `ERROR`.
* `task_state` - In-flight OpenStack task; empty when settled.
* `image` / `image_os_type` - Image the VM was built from, and its OS family.
* `ssh_key` - Name of the injected SSH key.
* `flavor_name` / `vcpus` / `ram` - Resolved sizing.
* `created_at` / `last_modified` - RFC 3339 timestamps.

-> The flavor ID, networks and block devices are not returned by the API's details endpoint, so
they are not available here.
