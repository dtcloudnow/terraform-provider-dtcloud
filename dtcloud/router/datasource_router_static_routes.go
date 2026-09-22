package router

import (
	"context"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// DataSourceDtcloudRouterStaticRoutes lists the static routes on a router,
// including any added outside Terraform.
func DataSourceDtcloudRouterStaticRoutes() *schema.Resource {
	return &schema.Resource{
		Description: "Lists the static routes on a router, including any added outside Terraform.",

		ReadContext: dataSourceDtcloudRouterStaticRoutesRead,
		Schema: map[string]*schema.Schema{
			"router_id": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the router whose static routes are listed.",
			},
			"routes": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The router's static routes, in the order the platform holds them.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"destination": {Type: schema.TypeString, Computed: true, Description: "Destination network in CIDR notation."},
						"next_hop":    {Type: schema.TypeString, Computed: true, Description: "Address traffic for the destination is sent to."},
					},
				},
			},
		},
	}
}

func dataSourceDtcloudRouterStaticRoutesRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	routerID := d.Get("router_id").(string)

	routes, _, err := client.Router.ListRouterStaticRoutes(ctx, routerID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("Router %q not found", routerID)
		}
		return diag.Errorf("Error listing the static routes of router %q: %s", routerID, err)
	}

	out := make([]interface{}, 0, len(routes))
	for _, route := range routes {
		out = append(out, map[string]interface{}{
			"destination": route.DestinationSubnet,
			"next_hop":    route.NextHop,
		})
	}

	if err := d.Set("routes", out); err != nil {
		return diag.FromErr(err)
	}

	d.SetId(routerID)
	return nil
}
