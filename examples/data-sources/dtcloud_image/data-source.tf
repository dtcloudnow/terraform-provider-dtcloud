# By name. The lookup refuses an ambiguous answer rather than picking the first
# match, because image names are not unique on the platform.
data "dtcloud_image" "ubuntu" {
  name = "Ubuntu 22.04"
}

data "dtcloud_image" "by_id" {
  id = var.image_id
}
