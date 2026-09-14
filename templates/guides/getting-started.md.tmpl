---
# Guides are hand-written. tfplugindocs copies this file to docs/guides/ on
# `make docs`; editing docs/guides/ directly is lost on the next run.
page_title: "Getting started with the dtcloud provider"
subcategory: "Guides"
---

# Getting started

This walks through building your first machine on DT Cloud with Terraform: what you need before
you start, what a minimal configuration looks like, and what to expect from each command. It
assumes no prior Terraform.

## What Terraform does here

You write a file describing what you want. Terraform works out the difference between that and
what exists, then makes the calls to close the gap.

That is the whole idea, and it is what makes it different from a script that calls the API. A
script says *do these steps*. Terraform says *this is the result I want* — so running it twice
changes nothing the second time, and running it after someone edited things by hand puts them
back.

## 1. Install Terraform

Download it from [releases.hashicorp.com](https://releases.hashicorp.com/terraform/) and put the
binary on your `PATH`.

```sh
terraform version
```

## 2. Get your credentials

You need three things from the DT Cloud console: an **API access key**, an **API secret key**,
and the **region (server) id** your project lives in.

Pick whichever way suits you — the provider takes them from the first place that has them.

### Configure the machine once (recommended)

```sh
terraform-provider-dtcloud configure
```

It asks for the three values, checks them against the API before saving anything, and writes
them to your user configuration directory with owner-only permissions. Every project on the
machine then works with an empty provider block and no credentials in sight.

The command is the provider binary itself, so there is nothing extra to install. If it is not
on your `PATH`, the error you get from `terraform plan` prints the full path to it.

### Or let Terraform ask, per project

Declare the values as variables with no default and Terraform prompts for them on the first
`plan`:

```hcl
variable "dtcloud_access_key" {
  type      = string
  sensitive = true
}

variable "dtcloud_secret_key" {
  type      = string
  sensitive = true
}

variable "dtcloud_region_id" {
  type = string
}

provider "dtcloud" {
  access_key = var.dtcloud_access_key
  secret_key = var.dtcloud_secret_key
  region_id  = var.dtcloud_region_id
}
```

Put the values in `terraform.tfvars` once you tire of typing them, and add that file to your
`.gitignore`.

### Or environment variables, which is what CI should use

```sh
export DTCLOUD_ACCESS_KEY="..."
export DTCLOUD_SECRET_KEY="..."
export DTCLOUD_REGION_ID="2"
```

These override the configuration file, so a build agent never picks up a developer's account.

~> **Keep credentials out of your `.tf` files.** Anything written there goes into version
control, and `terraform.tfstate` is not encrypted either. Any of the three ways above keeps
both clean.

-> Keep an empty `provider "dtcloud" {}` block in your configuration even when it needs no
arguments. Without one, a credentials problem is reported by Terraform first as *"requires
explicit configuration"*, which points you the wrong way.

## 3. Find the ids you will need

A VM needs a flavor, an image, a network, a security group and a storage policy. Look them up
once and note the ids:

```hcl
data "dtcloud_networks" "virtual" {
  network_type = "Virtual"
}

output "networks" {
  value = [for n in data.dtcloud_networks.virtual.networks : "${n.name} ${n.id} ${n.cidr}"]
}
```

```sh
terraform apply    # data sources only — nothing is created
terraform output networks
```

## 4. Write the configuration

Create `main.tf`:

```hcl
terraform {
  required_providers {
    dtcloud = {
      source  = "dtcloudnow/dtcloud"
      version = "~> 0.1"
    }
  }
}

provider "dtcloud" {}

resource "dtcloud_ssh_key" "mine" {
  name       = "my-key"
  public_key = file("~/.ssh/id_rsa.pub")
}

resource "dtcloud_vm" "first" {
  name      = "my-first-vm"
  flavor_id = "<flavor id>"
  key_name  = dtcloud_ssh_key.mine.name

  network {
    uuid            = "<network id>"
    security_groups = ["<security group id>"]

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
    volume_type           = "<storage policy>"
    uuid                  = "<image id>"
  }
}

output "address" {
  value = dtcloud_vm.first.primary_ip
}
```

Two things that trip people up on the first try:

* **At least one of `key_name`, `user_data` or `script` is required.** They are the only ways
  in; a VM with none of the three boots with no way to log into it.
* **Leave `fixed_ip` out unless you want a specific address.** The provider then asks for one
  address and the platform allocates it; `primary_ip` reports it after apply. Without
  `security_groups` the interface gets the project's default group.

## 5. Run it

```sh
terraform init      # download the provider — once per directory
terraform plan      # show what would change; creates nothing
terraform apply     # do it, after you confirm
```

Read the plan before confirming. The symbols are the whole story:

| Symbol | Meaning |
|--------|---------|
| `+` | will be created |
| `~` | will be changed in place |
| `-` | **will be destroyed** |
| `-/+` | will be **destroyed and recreated** |

`-/+` is the one to slow down for. It means an argument changed that the platform cannot change
on a live instance, so the only way to reach the state you asked for is to rebuild. Every
resource page lists which arguments do that.

## 6. Change something

Edit `name` in the file and run `terraform plan` again. You will see `~` — a rename happens in
place. Now try changing the network id: that shows `-/+`, because it cannot.

## 7. Tear it down

```sh
terraform destroy
```

This deletes everything in the configuration. There is no undo.

## Adopting machines that already exist

If a VM was built in the console and you want Terraform to manage it, `terraform import` writes
it into state without touching the machine:

```sh
terraform import dtcloud_vm.first <vm-id>
terraform plan
```

The plan afterwards is the important part. Terraform fills in what the API reports. Anything it
cannot read stays empty in state, and when such an argument forces a new resource, the first plan
proposes to rebuild the machine. Each resource page lists those arguments and how to keep the
imported resource.

## Where to go next

* Every resource and data source has its own page with arguments, attributes, timeouts and
  import format.
* `examples/` in the provider repository holds working configurations for each service —
  these are the same files used to test the provider against the live platform.
