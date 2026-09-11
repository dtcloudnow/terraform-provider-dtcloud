data "dtcloud_vm_history" "web" {
  vm_id = dtcloud_vm.web.id
}

output "last_action" {
  value = data.dtcloud_vm_history.web.entries[0].action
}
