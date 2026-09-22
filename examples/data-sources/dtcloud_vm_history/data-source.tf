data "dtcloud_vm_history" "web" {
  vm_id = dtcloud_vm.web.id
}

# Newest first, so the first entry is the most recent action.
output "last_action" {
  value = data.dtcloud_vm_history.web.entries[0].activity
}
