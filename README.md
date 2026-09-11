# terraform-provider-dtcloud

Terraform provider for DT Cloud (CMP), built on the `dt-go` SDK.

Current scope: SSH keys, virtual machines, networks, security groups, elastic IPs, volumes, snapshots, images, plus read-only
catalogue and account data sources (flavors, regions, projects, quotas).

| Resources                      | Data sources               |
|--------------------------------|----------------------------|
| `dtcloud_ssh_key`              | `dtcloud_ssh_key`          |
| `dtcloud_vm`                   | `dtcloud_ssh_keys`         |
| `dtcloud_vm_volume_attachment` | `dtcloud_vm`               |
| `dtcloud_vm_network_interface` | `dtcloud_vms`              |
| `dtcloud_network`              | `dtcloud_vm_history`       |
| `dtcloud_security_group`       | `dtcloud_vm_history_entry` |
| `dtcloud_security_group_rule`  | `dtcloud_network`          |
| `dtcloud_elastic_ip`           | `dtcloud_networks`         |
| `dtcloud_volume`               | `dtcloud_security_group`   |
| `dtcloud_snapshot`             | `dtcloud_security_groups`  |
| `dtcloud_image`                | `dtcloud_my_ip`            |
|                                | `dtcloud_elastic_ip`       |
|                                | `dtcloud_elastic_ips`      |
|                                | `dtcloud_volume`           |
|                                | `dtcloud_volumes`          |
|                                | `dtcloud_volume_snapshots` |
|                                | `dtcloud_storage_policies` |
|                                | `dtcloud_snapshot`         |
|                                | `dtcloud_snapshots`        |
|                                | `dtcloud_image`            |
|                                | `dtcloud_images`           |
|                                | `dtcloud_image_versions`   |
|                                | `dtcloud_flavors`          |
|                                | `dtcloud_regions`          |
|                                | `dtcloud_projects`         |
|                                | `dtcloud_project_quotas`   |
|                                | `dtcloud_project_limits`   |

## Dependency chain

```
terraform-provider-dtcloud  ->  dt-go  ->  DT Cloud API
```

## Local development build

The remote `dt-go` is not yet API-aligned, so `go.mod` uses a `replace`
directive pointing at the local `../dt-go` checkout. **Before pushing**, remove
that replace and pin the published module version.

```sh
make build          # go install -> $GOPATH/bin/terraform-provider-dtcloud
```

Then register the local binary with Terraform via `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "dtcloudnow/dtcloud" = "/path/to/your/go/bin"   # go env GOPATH
  }
  direct {}
}
```

## Configuration

Provider settings (all support environment-variable fallbacks):

| Argument       | Env var             | Notes                                   |
|----------------|---------------------|-----------------------------------------|
| `access_key`   | `DTCLOUD_ACCESS_KEY`| Sent as `x-api-access-key`.             |
| `secret_key`   | `DTCLOUD_SECRET_KEY`| Sensitive. Sent as `x-api-secret-key`.  |
| `api_endpoint` | `DTCLOUD_API_URL`   | Base URL of your DT Cloud API.          |
| `region_id`    | `DTCLOUD_REGION_ID` | Sent as the `serverId` query param.     |

There are four ways to supply them, in precedence order, and **none needs a second tool
installed**:

1. **Let Terraform ask.** Declare `variable` blocks with no default and reference them from the
   provider block — Terraform prompts for anything it does not have. Put the values in
   `terraform.tfvars` (gitignored) to stop being asked on every run.
2. **`terraform-provider-dtcloud configure`.** The provider binary doubles as its own setup
   command: it prompts, verifies the credentials against the API before saving, and writes
   `config.yaml` into the OS configuration directory. `provider "dtcloud" {}` then needs nothing
   else. `-profile prod` writes a second account; flags make it scriptable.
3. **Environment variables** — the table above. The right answer in CI, and they override the
   file so a build agent never inherits a developer's account.
4. **The configuration file**, written by hand if you prefer:

```
Linux    ~/.config/terraform-provider-dtcloud/config.yaml
macOS    ~/Library/Application Support/terraform-provider-dtcloud/config.yaml
Windows  %AppData%\terraform-provider-dtcloud\config.yaml
```

```yaml
api:
  access_key: "..."
  secret_key: "..."
  base_url: https://cms.dt.net.tr/api/v1
region_id: 2
```

It holds a secret key, so keep it owner-readable only — `configure` does that for you, and the
provider tightens a too-permissive file at its own default path. Never commit credentials to
HCL. See `docs/index.md` for the full reference.

## Try it

`examples/` has two kinds of configuration. `examples/resources/` and
`examples/data-sources/` hold one minimal, per-type example each -- these are what the
generated documentation embeds, so they are always in step with the schema.
`examples/scenarios/` holds the multi-resource walkthroughs:
[`ssh-key`](examples/scenarios/ssh-key), [`vm`](examples/scenarios/vm),
[`network`](examples/scenarios/network),
[`security-group`](examples/scenarios/security-group) and
[`elastic-ip`](examples/scenarios/elastic-ip). With
the env vars exported and the `dev_overrides` in place — note there is no
`terraform init`, since `dev_overrides` bypasses provider installation:

```sh
cd examples/scenarios/ssh-key
terraform plan
terraform apply
```

## Documentation

Nothing under `docs/` is written by hand. The provider schema is the source of truth and the
pages are generated from it:

```
Go schema + examples/  --tfplugindocs-->  docs/  --cmd/gendoc-->  docusaurus (en + tr)
   (Description strings)                (Registry format)         (docs.dtcloudnow.com)
```

| Command                | What it does                                                        |
|------------------------|---------------------------------------------------------------------|
| `make docs`            | Regenerates `docs/` from the schema, examples and templates.        |
| `make docs_validate`   | Checks `docs/` against the Terraform Registry's rules.              |
| `make docs_check`      | Regenerates and fails if the result differs from the commit.        |
| `make docusaurus`      | Converts `docs/` into Docusaurus pages in `../docusaurus`.          |
| `make docs_all`        | All of the above, in order. This is what CI runs.                   |

So to change what a page says, change one of:

* the `Description` strings on the resource, data source or field in `dtcloud/` -- this is
  where nearly all page text lives, and keeping it next to the code is what stops the docs
  drifting from it;
* the example under `examples/resources/<type>/` or `examples/data-sources/<type>/`, which is
  embedded as the page's *Example Usage*, and `import.sh` alongside it, which becomes the
  *Import* section;
* `templates/index.md.tmpl` for the provider landing page, or `templates/guides/` for guides;
* `templates/resources.md.tmpl` / `templates/data-sources.md.tmpl` for the page layout shared
  by every type, including the `subcategory` grouping.

Then run `make docs` and commit the result. A new resource needs no template of its own.

CI runs `docs_check` on every push, so a schema change with no regenerated docs behind it
fails the pipeline. On the default branch it also converts `docs/` for the Docusaurus site and
opens a merge request there, which a human approves -- the same flow `dt-cli` uses for the
`dtctl` CLI reference.

## Tests

The acceptance tests run against fake API servers built into each service
package, so they need no credentials and no network:

```sh
TF_ACC=1 go test ./dtcloud/...
```

Without `TF_ACC=1` the SDK skips them and still prints `ok`, so use `-v` when
you want to see what actually ran.
