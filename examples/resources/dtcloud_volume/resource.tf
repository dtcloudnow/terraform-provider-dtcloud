# A blank data disk.
resource "dtcloud_volume" "data" {
  name           = "app-data"
  size           = 100
  storage_policy = data.dtcloud_storage_policies.available.policies[0].name
}

# A volume seeded from an image. The source is fixed at create time: changing it
# replaces the volume.
resource "dtcloud_volume" "boot" {
  name           = "web-01-boot"
  size           = 40
  storage_policy = "standard"
  image_id       = var.image_id
}
