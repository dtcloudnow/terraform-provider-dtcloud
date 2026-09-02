---
page_title: "Provider: dtcloud"
subcategory: ""
---

# dtcloud Provider

Manage DT Cloud (CMP) resources with Terraform: describe what you want in a configuration
file, run `terraform apply`, and the provider makes the platform match it.

## Example Usage

```hcl
terraform {
  required_providers {
    dtcloud = {
      source  = "dtcloudnow/dtcloud"
      version = "~> 0.1"
    }
  }
}

# Credentials come from the environment; see below.
provider "dtcloud" {}

resource "dtcloud_ssh_key" "default" {
  name       = "my-key"
  public_key = file("~/.ssh/id_rsa.pub")
}

resource "dtcloud_vm" "web" {
  name      = "web-01"
  flavor_id = var.flavor_id
  key_name  = dtcloud_ssh_key.default.name

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
    volume_type           = "standard"
    uuid                  = var.image_id
  }
}

output "address" {
  value = dtcloud_vm.web.primary_ip
}
```

## Authentication

The provider authenticates with an API key pair and always operates against one region.

```sh
export DTCLOUD_ACCESS_KEY="..."     # sent as the x-api-access-key header
export DTCLOUD_SECRET_KEY="..."     # sent as the x-api-secret-key header
export DTCLOUD_REGION_ID="1"        # sent as the serverId query parameter
```

Environment variables are the recommended way: credentials in a `.tf` file end up in version
control, and `terraform.tfstate` is not encrypted either.

## Argument Reference

* `access_key` - (Optional) API access key. Falls back to `DTCLOUD_ACCESS_KEY`.
* `secret_key` - (Optional, sensitive) API secret key. Falls back to `DTCLOUD_SECRET_KEY`.
* `region_id` - (Optional) Region / server id, sent with every request. Falls back to
  `DTCLOUD_REGION_ID`.
* `api_endpoint` - (Optional) Base URL of the API, e.g. `https://cms.dt.net.tr/api/v1`. Falls
  back to `DTCLOUD_API_URL`, then to the SDK's built-in default.

Both `access_key` and `secret_key` must be set; supplying one without the other fails
immediately with a clear message rather than an obscure API error later.

## Resources and data sources

Every page in this documentation is one of two kinds, and the difference decides what
Terraform is allowed to do to the thing it describes.

**A `resource` is something Terraform owns.** It creates it, records it in state, keeps it
matching your configuration, and **destroys it when you remove it from your configuration**.

```hcl
resource "dtcloud_vm" "web" {
  name = "web-01"
  # ...
}
```

**A `data` source only reads.** It looks something up so you can use its values, and it never
creates, changes or deletes anything.

```hcl
data "dtcloud_vm" "existing" {
  id = "9f1c2b3a-0000-4a1b-8c2d-1234567890ab"
}
```

Two practical consequences:

* Use a data source to refer to something Terraform did not create — a network, an image, a VM
  another team manages. Writing it as a `resource` instead would make Terraform believe it owns
  it, and `terraform destroy` would take it down with everything else.
* A data source is read on every plan, so its values are always current. A resource's
  attributes come from its last refresh.

The same name can exist as both — `dtcloud_vm` is a resource *and* a data source. They are
different things: `resource "dtcloud_vm"` builds a VM, `data "dtcloud_vm"` looks one up.

### What is available

| Resources | Data sources |
|-----------|--------------|
| `dtcloud_ssh_key` | `dtcloud_ssh_key` |
| `dtcloud_vm` | `dtcloud_vm` |
| `dtcloud_vm_volume_attachment` | `dtcloud_vms` |
| `dtcloud_vm_network_interface` | `dtcloud_vm_history` |
| `dtcloud_network` | `dtcloud_vm_history_entry` |
| `dtcloud_volume` | `dtcloud_ssh_keys` |
| | `dtcloud_network` |
| | `dtcloud_networks` |
| | `dtcloud_volume` |
| | `dtcloud_volumes` |
| | `dtcloud_volume_snapshots` |
| | `dtcloud_storage_policies` |

## How a resource maps to API calls

A resource page does not list API operations, because Terraform's model does not work that
way. You describe the end state; the provider decides which calls to make.

| You do | Terraform does | The provider calls |
|--------|----------------|--------------------|
| Add a resource block | Create | `POST` |
| Run `plan` or `apply` | Read (refresh) | `GET` |
| Change an argument | Update, or replace if the argument is `ForceNew` | `PUT` / action endpoints, or `DELETE` + `POST` |
| Remove the block, or `terraform destroy` | Delete | `DELETE` |

So each resource page documents its **arguments** (what you can set), its **attributes** (what
you can read back), and, where it matters, which arguments can change in place and which force
a replacement. That last part is the one to read before changing a live system.

## Importing existing infrastructure

Anything created outside Terraform can be brought under management with `terraform import`,
which writes it into state without touching the resource itself. Each resource page documents
its id format. Import recovers what the API reports; arguments the API does not echo back have
to be written into your configuration by hand, or the next plan proposes a replacement.
