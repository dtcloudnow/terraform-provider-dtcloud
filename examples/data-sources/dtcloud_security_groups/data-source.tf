data "dtcloud_security_groups" "all" {}

data "dtcloud_security_groups" "named_web" {
  name = "web-tier"
}
