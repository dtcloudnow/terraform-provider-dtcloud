package flavor

import (
	"context"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudFlavors lists the compute flavors available for VMs.
func DataSourceDtcloudFlavors() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceDtcloudFlavorsRead,
		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Optional: true,
				Description: "Only return flavors whose name matches exactly. Applied here, not " +
					"by the API — the endpoint takes no filters, so the full list is fetched either way.",
			},
			"flavors": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The flavors that matched.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":    {Type: schema.TypeString, Computed: true, Description: "Use this as a VM's flavor_id."},
						"name":  {Type: schema.TypeString, Computed: true},
						"vcpus": {Type: schema.TypeInt, Computed: true},
						"ram":   {Type: schema.TypeString, Computed: true, Description: "Reported as text, e.g. \"1 GB\"."},
					},
				},
			},
		},
	}
}

func dataSourceDtcloudFlavorsRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	flavors, _, err := client.Flavor.ListFlavors(ctx, nil)
	if err != nil {
		return diag.Errorf("Error listing flavors: %s", err)
	}

	wanted := d.Get("name").(string)
	out := make([]interface{}, 0, len(flavors))
	ids := make([]string, 0, len(flavors))
	for _, f := range flavors {
		if wanted != "" && !strings.EqualFold(f.Name, wanted) {
			continue
		}
		out = append(out, map[string]interface{}{
			"id": f.ID, "name": f.Name, "vcpus": f.VCPUs, "ram": f.RAM,
		})
		ids = append(ids, f.ID)
	}

	if err := d.Set("flavors", out); err != nil {
		return diag.FromErr(err)
	}
	d.SetId(hashIDs(append([]string{"vm", wanted}, ids...)))

	return nil
}
