package dtcloud

import (
	"context"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/image"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/network"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/snapshot"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/sshkey"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/vm"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/volume"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// Provider returns the schema.Provider for dtcloud.
func Provider() *schema.Provider {
	p := &schema.Provider{
		Schema: map[string]*schema.Schema{
			"access_key": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("DTCLOUD_ACCESS_KEY", nil),
				Description: "API access key, sent as the x-api-access-key header.",
			},
			"secret_key": {
				Type:        schema.TypeString,
				Optional:    true,
				Sensitive:   true,
				DefaultFunc: schema.EnvDefaultFunc("DTCLOUD_SECRET_KEY", nil),
				Description: "API secret key, sent as the x-api-secret-key header.",
			},
			"api_endpoint": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("DTCLOUD_API_URL", nil),
				Description: "Base URL of the DT Cloud API, ending in /api/v1. If unset, the SDK default is used.",
			},
			"region_id": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("DTCLOUD_REGION_ID", nil),
				Description: "Region / server id, sent as the serverId query parameter on every request.",
			},
		},
		DataSourcesMap: map[string]*schema.Resource{
			"dtcloud_ssh_key":  sshkey.DataSourceDtcloudSSHKey(),
			"dtcloud_ssh_keys": sshkey.DataSourceDtcloudSSHKeys(),

			"dtcloud_vm":               vm.DataSourceDtcloudVM(),
			"dtcloud_vms":              vm.DataSourceDtcloudVMs(),
			"dtcloud_vm_history":       vm.DataSourceDtcloudVMHistory(),
			"dtcloud_vm_history_entry": vm.DataSourceDtcloudVMHistoryEntry(),

			"dtcloud_network":  network.DataSourceDtcloudNetwork(),
			"dtcloud_networks": network.DataSourceDtcloudNetworks(),

			"dtcloud_volume":           volume.DataSourceDtcloudVolume(),
			"dtcloud_volumes":          volume.DataSourceDtcloudVolumes(),
			"dtcloud_volume_snapshots": volume.DataSourceDtcloudVolumeSnapshots(),
			"dtcloud_storage_policies": volume.DataSourceDtcloudStoragePolicies(),

			"dtcloud_snapshot":  snapshot.DataSourceDtcloudSnapshot(),
			"dtcloud_snapshots": snapshot.DataSourceDtcloudSnapshots(),

			"dtcloud_image":          image.DataSourceDtcloudImage(),
			"dtcloud_images":         image.DataSourceDtcloudImages(),
			"dtcloud_image_versions": image.DataSourceDtcloudImageVersions(),
		},
		ResourcesMap: map[string]*schema.Resource{
			"dtcloud_ssh_key":              sshkey.ResourceDtcloudSSHKey(),
			"dtcloud_vm":                   vm.ResourceDtcloudVM(),
			"dtcloud_vm_volume_attachment": vm.ResourceDtcloudVMVolumeAttachment(),
			"dtcloud_vm_network_interface": vm.ResourceDtcloudVMNetworkInterface(),

			"dtcloud_network": network.ResourceDtcloudNetwork(),

			"dtcloud_volume": volume.ResourceDtcloudVolume(),

			"dtcloud_snapshot": snapshot.ResourceDtcloudSnapshot(),

			"dtcloud_image": image.ResourceDtcloudImage(),
		},
	}

	p.ConfigureContextFunc = func(ctx context.Context, d *schema.ResourceData) (interface{}, diag.Diagnostics) {
		return providerConfigure(d)
	}

	return p
}

func providerConfigure(d *schema.ResourceData) (interface{}, diag.Diagnostics) {
	conf := config.Config{
		AccessKey:   d.Get("access_key").(string),
		SecretKey:   d.Get("secret_key").(string),
		APIEndpoint: d.Get("api_endpoint").(string),
		RegionID:    d.Get("region_id").(string),
	}

	client, err := conf.Client()
	if err != nil {
		return nil, diag.FromErr(err)
	}
	return client, nil
}
