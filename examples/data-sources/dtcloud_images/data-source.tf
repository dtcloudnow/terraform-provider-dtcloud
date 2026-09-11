data "dtcloud_images" "private_active" {
  visibility = "private"
  status     = "active"
}

data "dtcloud_images" "ubuntu" {
  os_distro = "ubuntu"
}
