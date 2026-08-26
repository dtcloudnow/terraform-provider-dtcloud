---
page_title: "dtcloud: dtcloud_vms"
subcategory: "Compute"
---

# dtcloud_vms (Data Source)

Lists the virtual machines in the active region and project, with optional filters.

## Example Usage

```hcl
data "dtcloud_vms" "stopped" {
  status = "SHUTOFF"
}

output "stopped_names" {
  value = [for vm in data.dtcloud_vms.stopped.vms : vm.name]
}
```

## Argument Reference

* `status` - (Optional) Only return VMs in this status, e.g. `ACTIVE` or `SHUTOFF`.
* `task` - (Optional) Only return VMs with this in-flight task.
* `security_group_name` - (Optional) Only return VMs attached to this security group.

## Attributes Reference

* `vms` - The matching virtual machines, each with:
  * `id`, `name`, `status`, `task_state`
  * `ip_addresses` - Addresses assigned to the VM.
  * `vcpus`, `ram`, `storage`, `volume_size`

-> The list endpoint returns a lighter record than the details endpoint: no image, no SSH key,
no timestamps, no per-interface detail. Use the singular `dtcloud_vm` data source when you need
the full picture of one instance.

-> The data source's own `id` is a hash of the returned VM ids, so a list that has not changed
does not show up as a diff.
