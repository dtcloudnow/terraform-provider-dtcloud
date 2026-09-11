# Every running machine.
data "dtcloud_vms" "running" {
  status = "ACTIVE"
}

# Everything attached to one security group -- the usual way to find what a rule
# change is about to affect.
data "dtcloud_vms" "behind_web_sg" {
  security_group_name = "web-tier"
}
