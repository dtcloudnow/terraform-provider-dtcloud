# terraform-provider-dtcloud

Terraform provider for DT Cloud (CMP), built on the `dt-go` SDK.

Current scope: SSH keys, virtual machines, networks, volumes and snapshots.

| Resources                      | Data sources               |
|--------------------------------|----------------------------|
| `dtcloud_ssh_key`              | `dtcloud_ssh_key`          |
| `dtcloud_vm`                   | `dtcloud_ssh_keys`         |
| `dtcloud_vm_volume_attachment` | `dtcloud_vm`               |
| `dtcloud_vm_network_interface` | `dtcloud_vms`              |
| `dtcloud_network`              | `dtcloud_vm_history`       |
| `dtcloud_volume`               | `dtcloud_vm_history_entry` |
| `dtcloud_snapshot`             | `dtcloud_network`          |
| `dtcloud_image`                | `dtcloud_networks`         |
|                                | `dtcloud_volume`           |
|                                | `dtcloud_volumes`          |
|                                | `dtcloud_volume_snapshots` |
|                                | `dtcloud_storage_policies` |
|                                | `dtcloud_snapshot`         |
|                                | `dtcloud_snapshots`        |
|                                | `dtcloud_image`            |
|                                | `dtcloud_images`           |
|                                | `dtcloud_image_versions`   |

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

Supply credentials via environment variables rather than committed HCL.

## Try it

See [`examples/ssh-key`](examples/ssh-key) and [`examples/vm`](examples/vm). With
the env vars exported and the `dev_overrides` in place — note there is no
`terraform init`, since `dev_overrides` bypasses provider installation:

```sh
cd examples/ssh-key
terraform plan
terraform apply
```

## Tests

The acceptance tests run against fake API servers built into each service
package, so they need no credentials and no network:

```sh
TF_ACC=1 go test ./dtcloud/...
```

Without `TF_ACC=1` the SDK skips them and still prints `ok`, so use `-v` when
you want to see what actually ran.
