package network

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// listNetworkOptions is the query string the list endpoint accepts. Both
// filters are applied by the API, not here, so a filtered list is one call.
type listNetworkOptions struct {
	Name        string `url:"name,omitempty"`
	NetworkType string `url:"networkType,omitempty"`
}

// DataSourceDtcloudNetworks lists the networks visible to the caller.
//
// The list endpoint reports a flatter shape than the details one: it folds the
// subnet's CIDR and gateway up onto the network and reports DHCP as a word
// rather than a boolean. That is what is exposed here — reading the full subnet
// for every network would mean one extra call each.
func DataSourceDtcloudNetworks() *schema.Resource {
	return &schema.Resource{
		Description: "Lists the networks visible to the caller.\n\n" +
			"The list endpoint reports a flatter shape than the details one: it folds the subnet's CIDR " +
			"and gateway up onto the network and reports DHCP as a word rather than a boolean. That is " +
			"what is exposed here -- reading the full subnet for every network would mean one extra call " +
			"each.",

		ReadContext: dataSourceDtcloudNetworksRead,
		Schema: map[string]*schema.Schema{
			"name": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return networks whose name matches.",
			},
			"network_type": {
				Type:         schema.TypeString,
				Optional:     true,
				ValidateFunc: validation.StringInSlice([]string{"Virtual", "Physical"}, false),
				Description:  "Only return networks of this type: Virtual or Physical.",
			},
			"networks": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The networks that matched.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":                    {Type: schema.TypeString, Computed: true},
						"name":                  {Type: schema.TypeString, Computed: true},
						"network_type":          {Type: schema.TypeString, Computed: true},
						"subnet_id":             {Type: schema.TypeString, Computed: true},
						"cidr":                  {Type: schema.TypeString, Computed: true},
						"gateway":               {Type: schema.TypeString, Computed: true},
						"ipam":                  {Type: schema.TypeString, Computed: true, Description: "Enabled or Disabled."},
						"dhcp":                  {Type: schema.TypeString, Computed: true, Description: "Enabled or Disabled."},
						"port_security_enabled": {Type: schema.TypeBool, Computed: true},
					},
				},
			},
		},
	}
}

func dataSourceDtcloudNetworksRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	opt := listNetworkOptions{
		Name:        d.Get("name").(string),
		NetworkType: d.Get("network_type").(string),
	}

	networks, _, err := client.Network.ListNetworks(ctx, opt)
	if err != nil {
		return diag.Errorf("Error listing networks: %s", err)
	}

	out := make([]interface{}, 0, len(networks))
	ids := make([]string, 0, len(networks))
	for _, n := range networks {
		out = append(out, map[string]interface{}{
			"id":                    n.ID,
			"name":                  n.Name,
			"network_type":          n.Type,
			"subnet_id":             n.SubnetID,
			"cidr":                  n.Cidr,
			"gateway":               n.Gateway,
			"ipam":                  n.Ipam,
			"dhcp":                  n.Dhcp,
			"port_security_enabled": n.PortSecurityEnabled,
		})
		ids = append(ids, n.ID)
	}

	if err := d.Set("networks", out); err != nil {
		return diag.FromErr(err)
	}

	// A stable id keeps an unchanged result from showing up as a diff.
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	d.SetId(fmt.Sprintf("%x", sum[:8]))

	return nil
}
