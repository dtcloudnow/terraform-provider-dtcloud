# Changelog

## 26.0.0

First public release. The provider manages DT Cloud (CMP) infrastructure through
the `dt-go` SDK. Versions follow the DT Cloud release year: the major is the
year. The examples pin `~> 26.0.0`, which takes the patch releases of 26.0.0 and
leaves every larger step to a deliberate change.

NOTES:

* Credentials resolve highest first from the `provider` block, the `DTCLOUD_*`
  environment variables, and `~/.config/terraform-provider-dtcloud/config.yaml`,
  which `terraform-provider-dtcloud configure` writes. The environment wins over
  the file so a build agent never inherits a developer's account.
* `configure` defaults to the production endpoint. Pass `--api-url` to point it
  at another environment; credentials are verified against that endpoint before
  anything is written.
* Every resource supports `terraform import`.

FEATURES:

* **New Resource:** `dtcloud_elastic_ip`
* **New Resource:** `dtcloud_image`
* **New Resource:** `dtcloud_lb`
* **New Resource:** `dtcloud_lb_balancing_pool`
* **New Resource:** `dtcloud_lb_health_monitor`
* **New Resource:** `dtcloud_lb_listener`
* **New Resource:** `dtcloud_lb_member`
* **New Resource:** `dtcloud_lb_pool`
* **New Resource:** `dtcloud_network`
* **New Resource:** `dtcloud_router`
* **New Resource:** `dtcloud_router_interface`
* **New Resource:** `dtcloud_router_static_route`
* **New Resource:** `dtcloud_security_group`
* **New Resource:** `dtcloud_security_group_rule`
* **New Resource:** `dtcloud_snapshot`
* **New Resource:** `dtcloud_ssh_key`
* **New Resource:** `dtcloud_vm`
* **New Resource:** `dtcloud_vm_network_interface`
* **New Resource:** `dtcloud_vm_volume_attachment`
* **New Resource:** `dtcloud_volume`
* **New Data Source:** `dtcloud_elastic_ip`
* **New Data Source:** `dtcloud_elastic_ips`
* **New Data Source:** `dtcloud_flavors`
* **New Data Source:** `dtcloud_image`
* **New Data Source:** `dtcloud_image_versions`
* **New Data Source:** `dtcloud_images`
* **New Data Source:** `dtcloud_lb`
* **New Data Source:** `dtcloud_lb_flavors`
* **New Data Source:** `dtcloud_lb_vms`
* **New Data Source:** `dtcloud_lbs`
* **New Data Source:** `dtcloud_my_ip`
* **New Data Source:** `dtcloud_network`
* **New Data Source:** `dtcloud_networks`
* **New Data Source:** `dtcloud_project_limits`
* **New Data Source:** `dtcloud_project_quotas`
* **New Data Source:** `dtcloud_projects`
* **New Data Source:** `dtcloud_regions`
* **New Data Source:** `dtcloud_router`
* **New Data Source:** `dtcloud_router_interfaces`
* **New Data Source:** `dtcloud_router_static_routes`
* **New Data Source:** `dtcloud_routers`
* **New Data Source:** `dtcloud_security_group`
* **New Data Source:** `dtcloud_security_groups`
* **New Data Source:** `dtcloud_snapshot`
* **New Data Source:** `dtcloud_snapshots`
* **New Data Source:** `dtcloud_ssh_key`
* **New Data Source:** `dtcloud_ssh_keys`
* **New Data Source:** `dtcloud_storage_policies`
* **New Data Source:** `dtcloud_vm`
* **New Data Source:** `dtcloud_vm_history`
* **New Data Source:** `dtcloud_vm_history_entry`
* **New Data Source:** `dtcloud_vms`
* **New Data Source:** `dtcloud_volume`
* **New Data Source:** `dtcloud_volume_snapshots`
* **New Data Source:** `dtcloud_volumes`
