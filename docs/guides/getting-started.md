---
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

You need three things from the DT Cloud console:

| Value | Environment variable |
|-------|----------------------|
| API access key | `DTCLOUD_ACCESS_KEY` |
| API secret key | `DTCLOUD_SECRET_KEY` |
| Region (server) id | `DTCLOUD_REGION_ID` |

```sh
export DTCLOUD_ACCESS_KEY="..."
export DTCLOUD_SECRET_KEY="..."
export DTCLOUD_REGION_ID="1"
```

~> **Keep these out of your `.tf` files.** Anything you write there goes into version control,
and `terraform.tfstate` is not encrypted either. Environment variables keep both clean.

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

* **`security_groups` and `fixed_ip` are required on every interface.** An interface built
  without a fixed IP comes up with no address and the machine is unreachable, so Terraform
  refuses at plan time rather than letting you build it.
* **At least one of `key_name`, `user_data` or `script` is required.** They are the only ways
  in; a VM with none of the three boots with no way to log into it.

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

The plan afterwards is the important part. Terraform fills in what the API reports; anything it
cannot read has to be written into your configuration by hand, or the first apply will propose
to rebuild the machine. Each resource page lists exactly which arguments those are.

## Where to go next

* Every resource and data source has its own page with arguments, attributes, timeouts and
  import format.
* `examples/` in the provider repository holds working configurations for each service —
  these are the same files used to test the provider against the live platform.
