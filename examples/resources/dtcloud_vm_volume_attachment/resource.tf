# The volume and the machine each have their own resource; this is only the
# attachment between them, so the volume can be detached and re-attached
# elsewhere without either being rebuilt.
resource "dtcloud_vm_volume_attachment" "data" {
  vm_id     = dtcloud_vm.web.id
  volume_id = dtcloud_volume.data.id
}
