data "dtcloud_ssh_keys" "all" {}

output "key_names" {
  value = data.dtcloud_ssh_keys.all.names
}
