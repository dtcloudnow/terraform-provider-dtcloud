data "dtcloud_regions" "all" {}

output "region_ids" {
  value = data.dtcloud_regions.all.ids
}
