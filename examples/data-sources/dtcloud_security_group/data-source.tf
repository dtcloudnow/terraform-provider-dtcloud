# By id.
data "dtcloud_security_group" "web" {
  id = var.security_group_id
}

# Or by name -- the lookup fails rather than guessing if the name matches more
# than one group.
data "dtcloud_security_group" "app" {
  name = "app-tier"
}
