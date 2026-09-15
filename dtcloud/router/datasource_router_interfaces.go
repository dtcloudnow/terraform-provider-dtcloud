package router

import (
	"context"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// DataSourceDtcloudRouterInterfaces lists everything attached to a router: the
// external gateway and the internal interfaces, in one list, as the API returns
// them.
//
// The `id` field means two different things depending on `type`, and there is
// no way to make it mean one — the endpoint reports a port id for an internal
// interface and a subnet id for the external gateway. `type` is therefore part
// of reading this list, not decoration. Only an internal interface's id can be
// used to detach anything.
func DataSourceDtcloudRouterInterfaces() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceDtcloudRouterInterfacesRead,
		Schema: map[string]*schema.Schema{
			"router_id": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the router whose interfaces are listed.",
			},
			"interfaces": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The router's interfaces, external gateway first.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id": {
							Type:     schema.TypeString,
							Computed: true,
							Description: "Port id when `type` is `Internal interface`, subnet id when it " +
								"is `External gateway`.",
						},
						"type": {
							Type:        schema.TypeString,
							Computed:    true,
							Description: "`External gateway` or `Internal interface`.",
						},
						"network_id": {
							Type:        schema.TypeString,
							Computed:    true,
							Description: "ID of the attached network. Not reported for the external gateway.",
						},
						"network_name": {Type: schema.TypeString, Computed: true, Description: "Name of the attached network."},
						"subnet_id": {
							Type:        schema.TypeString,
							Computed:    true,
							Description: "ID of the subnet the address came from. Not reported for the external gateway.",
						},
						"ip_address": {Type: schema.TypeString, Computed: true, Description: "Address the interface holds."},
						"cidr":       {Type: schema.TypeString, Computed: true, Description: "CIDR of the attached network."},
						"status":     {Type: schema.TypeString, Computed: true, Description: "Status of the interface."},
					},
				},
			},
		},
	}
}

func dataSourceDtcloudRouterInterfacesRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	routerID := d.Get("router_id").(string)

	interfaces, _, err := client.Router.ListRouterInterfaces(ctx, routerID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("Router %q not found", routerID)
		}
		return diag.Errorf("Error listing the interfaces of router %q: %s", routerID, err)
	}

	out := make([]interface{}, 0, len(interfaces))
	for _, iface := range interfaces {
		out = append(out, map[string]interface{}{
			"id":           iface.ID,
			"type":         iface.Type,
			"network_id":   iface.NetworkID,
			"network_name": iface.Network,
			"subnet_id":    iface.SubnetID,
			"ip_address":   iface.IPAddress,
			"cidr":         iface.Cidr,
			"status":       iface.Status,
		})
	}

	if err := d.Set("interfaces", out); err != nil {
		return diag.FromErr(err)
	}

	d.SetId(routerID)
	return nil
}
