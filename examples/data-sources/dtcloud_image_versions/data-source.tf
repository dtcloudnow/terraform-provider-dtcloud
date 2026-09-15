# The platform's curated catalogue -- not the same list as dtcloud_images, and
# the only place that says which flavors an image may be built on.
data "dtcloud_image_versions" "ubuntu" {
  type = "ubuntu"
}
