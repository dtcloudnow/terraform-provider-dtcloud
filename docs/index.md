---
page_title: "Provider: dtcloud"
subcategory: ""
---

# dtcloud Provider

Manage DT Cloud (CMP) resources with Terraform: describe what you want in a configuration
file, run `terraform apply`, and the provider makes the platform match it.

## Example Usage

Copy this and run `terraform init` then `terraform plan`. Terraform will **ask** for the
credentials — variables with no default are prompted for, so there is nothing to set up first
and nothing to remember:

```hcl
terraform {
  required_providers {
    dtcloud = {
      source  = "dtcloudnow/dtcloud"
      version = "~> 0.1"
    }
  }
}

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

resource "dtcloud_security_group" "web" {
  name        = "web-tier"
  description = "HTTPS from anywhere"
}

resource "dtcloud_security_group_rule" "https" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 443
  port_range_max    = 443
  remote_ip_prefix  = "0.0.0.0/0"
}
```

```
$ terraform plan
var.dtcloud_access_key
  Enter a value: ****
var.dtcloud_secret_key
  Enter a value: ****
var.dtcloud_region_id
  Enter a value: 2
```

Once you are past trying it out, stop typing them every run — put the values in
`terraform.tfvars` (and in `.gitignore`), or configure the machine once so that every project
you write afterwards needs no credentials at all. Both are in **Authentication**, next.

## Authentication

The provider needs three things: an API access key, an API secret key, and a region id.

There are four ways to supply them, searched in this order — **the first one that supplies a
setting wins**. None of them requires installing anything beyond Terraform itself.

### The quickest start: let Terraform ask

Terraform prompts for any variable that has no value, so this is a complete, working setup with
no files to create and no setup command to run:

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

`terraform plan` asks for each one. To stop being asked every run, put them in
`terraform.tfvars` — Terraform loads it automatically, and it should be in your `.gitignore`:

```hcl
dtcloud_access_key = "..."
dtcloud_secret_key = "..."
dtcloud_region_id  = "2"
```

or export them as `TF_VAR_dtcloud_access_key` and so on.

### Set it once for every project: `configure`

If you would rather configure the machine once and leave every `.tf` file clean, run the
provider's own setup command. It prompts, **checks the credentials against the API before
saving**, and writes a file the provider reads from then on:

```sh
terraform-provider-dtcloud configure
```

```
API access key: ****
API secret key: ****
Region id: 2

Credentials verified.
Written to /home/you/.config/terraform-provider-dtcloud/config.yaml (owner-readable only).
```

Your configuration then needs nothing at all:

```hcl
provider "dtcloud" {}
```

**Keep that empty block even though Terraform does not strictly require it.** With no block
present, a configuration error is reported by Terraform first, as
*"Provider … requires explicit configuration. Add a provider block…"* — advice that sends you
the wrong way, since the fix is usually to run `configure` or set the environment variables.
The empty block costs one line and means the only error you see is the one that tells you what
to do.

The binary is the one Terraform already downloaded — there is no second tool to install. After
`terraform init` it is under `.terraform/providers/...`; the provider prints its full path in
the error you get when credentials are missing, so it can be pasted straight into a shell.

It takes flags too, for scripting a machine:

```sh
terraform-provider-dtcloud configure \
  -access-key "..." \
  -secret-key "..." \
  -region-id 2 \
  -profile prod
```

`-force` replaces an existing file, and saves even when the credentials cannot be verified —
which is what an air-gapped setup needs.

### CI: environment variables

```sh
export DTCLOUD_ACCESS_KEY="..."     # sent as the x-api-access-key header
export DTCLOUD_SECRET_KEY="..."     # sent as the x-api-secret-key header
export DTCLOUD_REGION_ID="2"        # sent as the serverId query parameter
```

No files, and the values come from the pipeline's own secret store. These override the
configuration file, so a build agent never picks up a developer's account by accident.

### The configuration file itself

`configure` writes it, but it is plain YAML and you can edit or write it by hand:

| | |
|---|---|
| Linux | `~/.config/terraform-provider-dtcloud/config.yaml` |
| macOS | `~/Library/Application Support/terraform-provider-dtcloud/config.yaml` |
| Windows | `%AppData%\terraform-provider-dtcloud\config.yaml` |

```yaml
api:
  access_key: "..."
  secret_key: "..."
  base_url: https://cms.dt.net.tr/api/v1
region_id: 2
```

For more than one account, use profiles:

```yaml
default_profile: dev

profiles:
  dev:
    api: {access_key: "...", secret_key: "..."}
    region_id: 2
  prod:
    api: {access_key: "...", secret_key: "..."}
    region_id: 1
```

```hcl
provider "dtcloud" {
  profile = "prod"
}
```

The file holds a secret key, so it is kept readable by you and nobody else. `configure` writes
it that way; if you write it by hand, `chmod 400` it (on Windows, `attrib +R`). The provider
tightens a too-permissive file at its own default path and tells you it did. A file you point
at with `config_file` is only ever read and never altered, since it may belong to something
else.

## Argument Reference

* `access_key` - (Optional) API access key. Falls back to `DTCLOUD_ACCESS_KEY`, then the file.
* `secret_key` - (Optional, sensitive) API secret key. Falls back to `DTCLOUD_SECRET_KEY`, then
  the file.
* `region_id` - (Optional) Region / server id, sent as `serverId` with every request. Falls back
  to `DTCLOUD_REGION_ID`, then the file. **Required** — the API rejects requests without it.
* `api_endpoint` - (Optional) Base URL of the API, e.g. `https://cms.dt.net.tr/api/v1`. Falls
  back to `DTCLOUD_API_URL`, then the file's `api.base_url`, then the SDK's built-in default.
* `config_file` - (Optional) Path to the configuration file. Falls back to
  `DTCLOUD_CONFIG_FILE`, then the default path above. A leading `~` is expanded.
* `profile` - (Optional) Which account in the file to use. Falls back to `DTCLOUD_PROFILE`, then
  the file's `default_profile`, then `default`.

Both `access_key` and `secret_key` must end up set, from wherever. Supplying one without the
other fails immediately, with a message listing all three ways to fix it, rather than an
obscure API error later.

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
| `dtcloud_vm` | `dtcloud_ssh_keys` |
| `dtcloud_vm_volume_attachment` | `dtcloud_vm` |
| `dtcloud_vm_network_interface` | `dtcloud_vms` |
| `dtcloud_network` | `dtcloud_vm_history` |
| `dtcloud_security_group` | `dtcloud_vm_history_entry` |
| `dtcloud_security_group_rule` | `dtcloud_network` |
| `dtcloud_elastic_ip` | `dtcloud_networks` |
| `dtcloud_volume` | `dtcloud_security_group` |
| `dtcloud_snapshot` | `dtcloud_security_groups` |
| `dtcloud_image` | `dtcloud_my_ip` |
|  | `dtcloud_elastic_ip` |
|  | `dtcloud_elastic_ips` |
|  | `dtcloud_volume` |
|  | `dtcloud_volumes` |
|  | `dtcloud_volume_snapshots` |
|  | `dtcloud_storage_policies` |
|  | `dtcloud_snapshot` |
|  | `dtcloud_snapshots` |
|  | `dtcloud_image` |
|  | `dtcloud_images` |
|  | `dtcloud_image_versions` |
|  | `dtcloud_flavors` |
|  | `dtcloud_regions` |
|  | `dtcloud_projects` |
|  | `dtcloud_project_quotas` |
|  | `dtcloud_project_limits` |

Read-only catalogue and account data sources, none of which have a resource counterpart —
flavors are defined by the operator, and the endpoints that would "change" a region or project
switch the caller's own session rather than any piece of infrastructure:

| | |
|---|---|
| `dtcloud_flavors` | compute sizing catalogue |
| `dtcloud_regions` | regions, and which services each offers |
| `dtcloud_projects` | projects, and which one the session is pointed at |
| `dtcloud_project_quotas` | usage against allowance |
| `dtcloud_project_limits` | the full quota table |

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
