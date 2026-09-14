# The most recent entry from dtcloud_vm_history, read in full to get its status.
data "dtcloud_vm_history_entry" "latest" {
  vm_id      = dtcloud_vm.web.id
  history_id = data.dtcloud_vm_history.web.entries[0].id
}

output "latest_status" {
  value = data.dtcloud_vm_history_entry.latest.status
}
