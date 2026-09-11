package network

import (
	"context"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// DataSourceDtcloudNetwork looks up one network by id, for networks this
// configuration does not own — the common case, since networks outlive the
// things attached to them.
func DataSourceDtcloudNetwork() *schema.Resource {
	return &schema.Resource{
		Description: "Looks up one network by id.\n\n" +
			"Use it to reference a network Terraform did not create -- the common case, since networks " +
			"tend to outlive the things attached to them. Declaring one as a resource instead would hand " +
			"Terraform ownership of it, and a later `terraform destroy` would take it down along with " +
			"everything else.",

		ReadContext: dataSourceDtcloudNetworkRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the network to look up.",
			},
			"name":         {Type: schema.TypeString, Computed: true, Description: "Name of the network."},
			"network_type": {Type: schema.TypeString, Computed: true, Description: "Virtual or Physical."},
			"ipam_enabled": {Type: schema.TypeBool, Computed: true, Description: "Whether the platform manages addressing, i.e. whether there is a subnet."},
			"subnet_id":    {Type: schema.TypeString, Computed: true, Description: "ID of the subnet, empty when IPAM is off."},
			"cidr":         {Type: schema.TypeString, Computed: true, Description: "Address range of the subnet."},
			"gateway_ip":   {Type: schema.TypeString, Computed: true, Description: "Gateway address of the subnet."},
			"enable_dhcp":  {Type: schema.TypeBool, Computed: true, Description: "Whether the subnet hands out addresses over DHCP."},
			"ip_version":   {Type: schema.TypeInt, Computed: true, Description: "IP version of the subnet."},
			"dns_nameservers": {
				Type: schema.TypeList, Computed: true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "DNS servers advertised on this network.",
			},
			"allocation_pools": {
				Type: schema.TypeList, Computed: true,
				Elem: &schema.Resource{Schema: map[string]*schema.Schema{
					"start": {Type: schema.TypeString, Computed: true},
					"end":   {Type: schema.TypeString, Computed: true},
				}},
				Description: "Address ranges the subnet allocates from.",
			},
		},
	}
}

func dataSourceDtcloudNetworkRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	id := d.Get("id").(string)

	details, _, err := client.Network.GetNetworkDetails(ctx, id)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("Network %q not found", id)
		}
		return diag.Errorf("Error retrieving network %q: %s", id, err)
	}
	if details.NetworkConfiguration.ID == "" {
		return diag.Errorf("Network %q not found", id)
	}

	d.SetId(details.NetworkConfiguration.ID)
	d.Set("name", details.NetworkConfiguration.Name)
	d.Set("network_type", details.NetworkConfiguration.Type)

	if details.Subnets == nil {
		d.Set("ipam_enabled", false)
		return nil
	}

	d.Set("ipam_enabled", true)
	d.Set("subnet_id", details.Subnets.ID)
	d.Set("cidr", details.Subnets.CIDR)
	d.Set("gateway_ip", details.Subnets.Gateway)
	d.Set("enable_dhcp", details.Subnets.DHCP)
	d.Set("ip_version", details.Subnets.SubnetIPVersion)
	d.Set("dns_nameservers", details.Subnets.DNSServer)
	d.Set("allocation_pools", flattenAllocationPools(details.Subnets.AllocationPools))

	return nil
}
