data "dtcloud_volumes" "all" {}

data "dtcloud_volumes" "on_web" {
  attached_to_id = dtcloud_vm.web.id
}
