package flavor

import (
	"context"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudLBFlavors lists the load balancer flavors, a separate
// catalogue from the compute ones. Every size exists twice, once per `ha`
// value, so a name alone does not identify a flavor.
func DataSourceDtcloudLBFlavors() *schema.Resource {
	return &schema.Resource{
		Description: "Lists the load balancer sizes available in the region, each name offered twice -- once without high availability and once with it.",

		ReadContext: dataSourceDtcloudLBFlavorsRead,
		Schema: map[string]*schema.Schema{
			"name": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return flavors whose name matches exactly. Applied here, not by the API.",
			},
			"ha": {
				Type:     schema.TypeBool,
				Optional: true,
				Description: "Only return flavors with this high-availability setting. Names repeat " +
					"across the two settings, so combine this with name to identify one flavor.",
			},
			"flavors": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The flavors that matched.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":    {Type: schema.TypeString, Computed: true, Description: "Use this as a load balancer's flavor_id."},
						"name":  {Type: schema.TypeString, Computed: true},
						"vcpus": {Type: schema.TypeInt, Computed: true},
						"ram":   {Type: schema.TypeString, Computed: true},
						"ha":    {Type: schema.TypeBool, Computed: true, Description: "Whether this flavor gives a highly available load balancer."},
					},
				},
			},
		},
	}
}

func dataSourceDtcloudLBFlavorsRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	flavors, _, err := client.Flavor.ListLoadBalancerFlavors(ctx, nil)
	if err != nil {
		return diag.Errorf("Error listing load balancer flavors: %s", err)
	}

	wanted := d.Get("name").(string)
	ha, haSet := d.GetOkExists("ha")

	out := make([]interface{}, 0, len(flavors))
	ids := make([]string, 0, len(flavors))
	for _, f := range flavors {
		if wanted != "" && !strings.EqualFold(f.Name, wanted) {
			continue
		}
		if haSet && f.HA != ha.(bool) {
			continue
		}
		out = append(out, map[string]interface{}{
			"id": f.ID, "name": f.Name, "vcpus": f.VCPUs, "ram": f.RAM, "ha": f.HA,
		})
		ids = append(ids, f.ID)
	}

	if err := d.Set("flavors", out); err != nil {
		return diag.FromErr(err)
	}
	d.SetId(hashIDs(append([]string{"lb", wanted}, ids...)))

	return nil
}
