resource "dtcloud_image" "golden" {
  name        = "golden-ubuntu-22.04"
  source_file = "${path.module}/images/ubuntu-22.04.qcow2"
  disk_format = "qcow2"
  os_distro   = "ubuntu"
  min_disk    = 20

  min_ram    = 2048
  visibility = "private"
  uefi       = false
  tags       = ["golden", "ubuntu"]
}
