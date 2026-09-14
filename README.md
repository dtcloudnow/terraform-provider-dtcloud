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

Nothing under `docs/` is written by hand. The pages are generated from the code, the examples
and one template per page, and the Docusaurus site in English and Turkish is generated from
`docs/`:

```
Go schema + examples/ + templates/  --tfplugindocs-->  docs/  --cmd/gendoc + i18n/tr-->  docusaurus (en + tr)
                                                  (Registry format)                     (docs.dtcloudnow.com)
```

| Command                | What it does                                                          |
|------------------------|-----------------------------------------------------------------------|
| `make docs`            | Regenerates `docs/` from the schema, examples and templates.          |
| `make docs_validate`   | Checks `docs/` against the Terraform Registry's rules.                |
| `make docs_check`      | Regenerates and fails if the result differs from the commit.          |
| `make docs_i18n_check` | Fails if a text has no Turkish translation, or one is no longer used. |
| `make docusaurus`      | Converts `docs/` into Docusaurus pages in `../docusaurus`.            |
| `make docs_all`        | Generate, validate, check translations, convert. This is what CI runs. |

Where each part of a page is edited:

| Part of the page                                     | Edited in                                                        |
|------------------------------------------------------|------------------------------------------------------------------|
| The summary sentence under the title                 | the resource's or data source's `Description` in `dtcloud/` -- one sentence |
| Every argument and attribute in *Schema*             | each field's `Description` in `dtcloud/`; `dtcloud/schema_docs.go` adds "Changing this forces a new resource to be created." to ForceNew arguments |
| *Example Usage* and the import command               | `examples/resources/<type>/` (`resource.tf`, `import.sh`) or `examples/data-sources/<type>/` |
| The subcategory, notes, *Behaviour worth knowing*, *Timeouts*, import notes | `templates/resources/<name>.md.tmpl` or `templates/data-sources/<name>.md.tmpl` |
| The Turkish text of all of the above                 | `i18n/tr/<resources or data-sources>/<name>.yaml`                |
| The provider page and the guides                     | `templates/index.md.tmpl`, `templates/guides/`, `i18n/tr/index.yaml`, `i18n/tr/guides/` |

Every page template has the same sections in the same order -- summary, *Example Usage*, notes,
*Schema*, *Behaviour worth knowing*, *Timeouts*, *Import* -- and leaves out a section with nothing
to say. What a template states about the code, such as timeout defaults or what an import reads
back, is written by hand, so re-check it when that code changes.

Adding a resource or data source therefore takes its `Description` strings, an example, and a page
template (copy a sibling's; `TestEveryTypeHasADocsTemplate` fails without one). Run `make docs`,
then `make docs_i18n_check`: it prints every English text that has no translation yet as YAML, to
be translated into `i18n/tr/`. Commit all of it together.

CI runs `docs_check` and `docs_i18n_check` on every push, so a schema change with no regenerated
docs or no translation behind it fails the pipeline. On the default branch it also converts `docs/` for the Docusaurus site and
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
