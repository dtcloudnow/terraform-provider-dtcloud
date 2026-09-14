data "dtcloud_storage_policies" "available" {}

output "policy_names" {
  value = data.dtcloud_storage_policies.available.names
}
