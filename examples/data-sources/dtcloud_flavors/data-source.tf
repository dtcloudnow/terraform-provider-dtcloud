data "dtcloud_flavors" "all" {}

# The endpoint takes no filters, so `name` is applied by the provider.
data "dtcloud_flavors" "small" {
  name = "s1.small"
}
