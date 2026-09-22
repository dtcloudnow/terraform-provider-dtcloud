# Terraform Provider for DT Cloud

Manage DT Cloud (CMP) infrastructure with Terraform: virtual machines, networks, routers,
security groups, elastic IPs, volumes, snapshots, images and load balancers, together with
read-only data sources for the catalogue and your account.

The full reference for every resource and data source is on the
[Terraform Registry](https://registry.terraform.io/providers/dtcloudnow/dtcloud/latest/docs).

## Requirements

- Terraform 1.0 or later
- A DT Cloud account with an API access key and secret key

## Getting started

Declare the provider:

```hcl
terraform {
  required_providers {
    dtcloud = {
      source  = "dtcloudnow/dtcloud"
      version = "~> 26.0.0"
    }
  }
}

provider "dtcloud" {}
```

`terraform init` downloads it. The provider is also its own setup command, so save your
credentials once:

```sh
terraform-provider-dtcloud configure
```

It asks for your access key, secret key and region, checks them against the API before saving
anything, and writes them to your user configuration directory with owner-only permissions.
Every project on the machine then works with the empty `provider` block above. If the command is
not on your `PATH`, run `terraform plan` without credentials: the error prints the full path to it.

Then describe what you want and apply it:

```hcl
resource "dtcloud_ssh_key" "me" {
  name       = "my-key"
  public_key = file("~/.ssh/id_rsa.pub")
}
```

```sh
terraform apply
```

## Credentials

| Argument       | Environment variable | Notes                                           |
|----------------|----------------------|-------------------------------------------------|
| `access_key`   | `DTCLOUD_ACCESS_KEY` |                                                 |
| `secret_key`   | `DTCLOUD_SECRET_KEY` | Sensitive.                                      |
| `api_endpoint` | `DTCLOUD_API_URL`    | Base URL of the API, ending in `/api/v1`.       |
| `region_id`    | `DTCLOUD_REGION_ID`  | The region every call is made in.               |

All four are required. There are three ways to supply them; values resolve highest first from
the `provider` block, the environment, and the configuration file. Whichever you use, keep
credentials out of your `.tf` files, which end up in version control.

### Save them once with `configure`

The way shown in [Getting started](#getting-started): the values go to a file in your user
configuration directory, and every project on the machine works with an empty `provider` block.
`configure` targets production unless given `--api-url`, and `--profile <name>` saves a second
account, selected with the `profile` argument or `DTCLOUD_PROFILE`.

### Let Terraform ask, per project

Declare the values as variables and pass them to the provider. Terraform asks for every variable
that has no default and no value yet:

```hcl
variable "dtcloud_access_key" {
  type = string
}

variable "dtcloud_secret_key" {
  type      = string
  sensitive = true
}

variable "dtcloud_region_id" {
  type = string
}

variable "dtcloud_api_url" {
  type    = string
  default = "https://console.dt.net.tr/api/v1"
}

provider "dtcloud" {
  access_key   = var.dtcloud_access_key
  secret_key   = var.dtcloud_secret_key
  region_id    = var.dtcloud_region_id
  api_endpoint = var.dtcloud_api_url
}
```

The endpoint has a default, so only the other three are asked for; set `dtcloud_api_url` for any
environment other than production. To stop being asked on every run, put the values in a
`terraform.tfvars` next to your configuration and add that file to `.gitignore`:

```hcl
dtcloud_access_key = "..."
dtcloud_secret_key = "..."
dtcloud_region_id  = "2"
```

### Environment variables

The right choice for CI. They override the configuration file, so a build agent never inherits a
developer's account:

```sh
export DTCLOUD_ACCESS_KEY="..."
export DTCLOUD_SECRET_KEY="..."
export DTCLOUD_REGION_ID="2"
export DTCLOUD_API_URL="https://console.dt.net.tr/api/v1"
```

## Examples

[`examples/scenarios/`](examples/scenarios) holds complete configurations for each service, from a
single SSH key to a load balancer in front of your virtual machines.

## License

[Apache License 2.0](LICENSE)
