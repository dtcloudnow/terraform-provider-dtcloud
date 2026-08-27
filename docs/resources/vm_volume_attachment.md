---
page_title: "dtcloud: dtcloud_vm_volume_attachment"
subcategory: "Compute"
---

# dtcloud_vm_volume_attachment

Attaches an existing volume to a virtual machine.

This is a separate resource rather than a block on `dtcloud_vm` because the attachment has its
own lifecycle: a volume can be detached and re-attached elsewhere without touching either the
VM or the volume.

## Example Usage

```hcl
resource "dtcloud_vm_volume_attachment" "data" {
  vm_id     = dtcloud_vm.web.id
  volume_id = "3f2a1b0c-0000-4a1b-8c2d-000000000009"
}
```

## Argument Reference

* `vm_id` - (Required) ID of the virtual machine. Changing this recreates the attachment.
* `volume_id` - (Required) ID of the existing volume. Changing this recreates the attachment.

The whole resource is `ForceNew`: an attachment can only be made or broken, never edited.

## Attributes Reference

* `id` - `<vm-id>:<volume-id>`.
* `volume_name` - Name of the attached volume.
* `size` - Size of the volume in GB.
* `storage_policy` - Storage policy / volume type.

## Behaviour worth knowing

-> This resource attaches volumes that **already exist**. The API also has a path that creates
a volume and attaches it in one call; that belongs to a future `dtcloud_volume` resource, so
that a volume's lifetime is owned by the resource that creates it rather than by an attachment.

~> The `volume` list on `dtcloud_vm` is a snapshot from that resource's last refresh. A volume
attached by this resource in the same `apply` will not appear there until the next refresh —
read this resource's attributes instead.

~> Detaching a volume that is mounted inside the guest can lose data. Terraform will not stop
the VM or unmount anything first.

## Timeouts

* `create` - Defaults to **10 minutes**.
* `delete` - Defaults to **10 minutes**.

Override them with a `timeouts` block:

```hcl
timeouts {
  create = "30m"
}
```

## Import

```
terraform import dtcloud_vm_volume_attachment.data <vm-id>:<volume-id>
```
