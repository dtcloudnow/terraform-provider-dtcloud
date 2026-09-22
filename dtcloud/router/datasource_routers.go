package router

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudRouters lists every router visible to the caller.
//
// The listing endpoint describes a router's gateway differently from the
// details endpoint the resource reads: it reports the external network by
// **name** and never sends its id, and it adds the CIDR of that network. Both
// values are passed through as they arrive. Use dtcloud_router when the id is
// what is needed.
//
// The endpoint takes no query parameters, so the filters below are applied by
// the provider after the list is fetched.
func DataSourceDtcloudRouters() *schema.Resource {
	return &schema.Resource{
		Description: "Lists every router visible to the caller.",

		ReadContext: dataSourceDtcloudRoutersRead,
		Schema: map[string]*schema.Schema{
			"name": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return routers with this exact name. Applied by the provider.",
			},
			"status": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return routers in this status, e.g. `ACTIVE`. Applied by the provider.",
			},
			"routers": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The routers that matched, in the order the platform returned them.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":     {Type: schema.TypeString, Computed: true},
						"name":   {Type: schema.TypeString, Computed: true},
						"status": {Type: schema.TypeString, Computed: true},
						"enable_snat": {
							Type:     schema.TypeBool,
							Computed: true,
							Description: "Whether the gateway masquerades traffic. Reported as false " +
								"for a router with no gateway at all — `is_external` tells the two apart.",
						},
						"external_network": {
							Type:     schema.TypeString,
							Computed: true,
							Description: "Name of the external network the gateway is on, or `-` when " +
								"there is no gateway. This endpoint does not report its id.",
						},
						"cidr": {
							Type:        schema.TypeString,
							Computed:    true,
							Description: "CIDR of the external network the gateway is on.",
						},
						"is_external": {
							Type:        schema.TypeBool,
							Computed:    true,
							Description: "Whether the router has an external gateway.",
						},
					},
				},
			},
		},
	}
}

func dataSourceDtcloudRoutersRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	list, _, err := client.Router.ListRouters(ctx, nil)
	if err != nil {
		return diag.Errorf("Error listing routers: %s", err)
	}

	nameFilter := d.Get("name").(string)
	statusFilter := d.Get("status").(string)

	out := make([]interface{}, 0, len(list))
	ids := make([]string, 0, len(list))
	for _, r := range list {
		if nameFilter != "" && r.Name != nameFilter {
			continue
		}
		if statusFilter != "" && !strings.EqualFold(r.Status, statusFilter) {
			continue
		}
		out = append(out, map[string]interface{}{
			"id":               r.ID,
			"name":             r.Name,
			"status":           r.Status,
			"enable_snat":      r.Snat,
			"external_network": r.ExternalNetwork,
			"cidr":             r.Cidr,
			"is_external":      r.IsExternal,
		})
		ids = append(ids, r.ID)
	}

	if err := d.Set("routers", out); err != nil {
		return diag.FromErr(err)
	}

	// A stable id keeps an unchanged result from showing up as a diff.
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	d.SetId(fmt.Sprintf("%x", sum[:8]))

	return nil
}
