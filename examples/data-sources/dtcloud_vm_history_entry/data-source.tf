data "dtcloud_vm_history_entry" "resize" {
  vm_id      = dtcloud_vm.web.id
  history_id = data.dtcloud_vm_history.web.entries[0].history_id
}
