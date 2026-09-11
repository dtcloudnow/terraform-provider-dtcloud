# An interface added after the machine was built. It has its own lifecycle and
# can be detached without recreating the VM.
resource "dtcloud_vm_network_interface" "storage" {
  vm_id           = dtcloud_vm.web.id
  network_id      = dtcloud_network.storage.id
  security_groups = [dtcloud_security_group.internal.id]
}
