terraform {
  required_providers {
    dtcloud = {
      source = "dtcloudnow/dtcloud"
    }
  }
}

# Credentials and region come from the environment:
#   export DTCLOUD_ACCESS_KEY=...
#   export DTCLOUD_SECRET_KEY=...
#   export DTCLOUD_REGION_ID=1
provider "dtcloud" {}

variable "flavor_id" {
  type        = string
  description = "Flavor to build the VM with. List them with `dtctl flavor list`."
}

variable "network_id" {
  type        = string
  description = "Network to attach the VM to. List them with `dtctl network list`."
}

variable "image_id" {
  type        = string
  description = "Image to boot from. List them with `dtctl image list`."
}

variable "security_group_id" {
  type        = string
  description = "Security group bound to the VM's interface. Required."
}

variable "volume_type" {
  type        = string
  description = "Storage policy the boot disk is created on. Required."
}

resource "dtcloud_ssh_key" "example" {
  name       = "terraform-vm-example"
  public_key = file("~/.ssh/id_rsa.pub")
}

resource "dtcloud_vm" "example" {
  name      = "terraform-vm-example"
  flavor_id = var.flavor_id
  key_name  = dtcloud_ssh_key.example.name

  # security_groups and at least one fixed_ip are required on every interface.
  # An interface with no fixed IP comes up with no address at all.
  network {
    uuid            = var.network_id
    security_groups = [var.security_group_id]

    fixed_ip {
      ip_version = 4
    }
  }

  block_device {
    boot_index            = 0
    volume_size           = 20
    source_type           = "image"
    device_type           = "disk"
    destination_type      = "volume"
    delete_on_termination = true
    volume_type           = var.volume_type
    uuid                  = var.image_id
  }
}

# Read the same VM back.
data "dtcloud_vm" "example" {
  id = dtcloud_vm.example.id
}

output "id" {
  value = dtcloud_vm.example.id
}

output "status" {
  value = data.dtcloud_vm.example.status
}

output "flavor" {
  value = "${data.dtcloud_vm.example.flavor_name} (${data.dtcloud_vm.example.vcpus} vCPU, ${data.dtcloud_vm.example.ram})"
}

# --- Attachments added after boot -------------------------------------------
# Interfaces and volumes declared inside dtcloud_vm live and die with the VM.
# These two resources manage the ones added afterwards, each with its own
# lifecycle.

variable "extra_network_id" {
  type        = string
  description = "Second network to attach to the VM. Leave empty to skip."
  default     = ""
}

variable "extra_volume_id" {
  type        = string
  description = "Existing volume to attach to the VM. Leave empty to skip."
  default     = ""
}

resource "dtcloud_vm_network_interface" "extra" {
  count = var.extra_network_id == "" ? 0 : 1

  vm_id      = dtcloud_vm.example.id
  network_id = var.extra_network_id

  fixed_ip {
    ip_version = 4
  }
}

resource "dtcloud_vm_volume_attachment" "extra" {
  count = var.extra_volume_id == "" ? 0 : 1

  vm_id     = dtcloud_vm.example.id
  volume_id = var.extra_volume_id
}

# All VMs in the active region, for comparison.
data "dtcloud_vms" "all" {
  depends_on = [dtcloud_vm.example]
}

output "address" {
  value = dtcloud_vm.example.primary_ip
}

output "attached_volumes" {
  value = [for v in data.dtcloud_vm.example.volume : "${v.name} (${v.size} GB)"]
}

output "vm_count_in_region" {
  value = length(data.dtcloud_vms.all.vms)
}
