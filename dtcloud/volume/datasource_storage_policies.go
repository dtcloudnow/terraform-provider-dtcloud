package volume

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudStoragePolicies lists the storage policies available in the
// region. The endpoint cross-references the project's storage quota, so it
// returns what this project can actually create on, not the whole catalogue.
//
// Pass `name`, not `id`, to `dtcloud_volume.storage_policy`: the volume details
// endpoint reports the policy by name, so an id would drift on every plan.
func DataSourceDtcloudStoragePolicies() *schema.Resource {
	return &schema.Resource{
		Description: "Lists the storage policies available in the region.",

		ReadContext: dataSourceDtcloudStoragePoliciesRead,
		Schema: map[string]*schema.Schema{
			"name": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return the policy with this exact name. Applied by the provider — the API has no filter.",
			},
			"policies": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "Storage policies this project has quota for.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":   {Type: schema.TypeString, Computed: true, Description: "ID of the volume type."},
						"name": {Type: schema.TypeString, Computed: true, Description: "Name of the policy — this is what dtcloud_volume.storage_policy takes."},
					},
				},
			},
			"names": {
				Type:        schema.TypeList,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "Just the policy names, for when all you need is to check one is available.",
			},
		},
	}
}

func dataSourceDtcloudStoragePoliciesRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	policies, _, err := client.Volume.ListStoragePolicies(ctx, nil)
	if err != nil {
		return diag.Errorf("Error listing storage policies: %s", err)
	}

	nameFilter := d.Get("name").(string)

	out := make([]interface{}, 0, len(policies))
	names := make([]string, 0, len(policies))
	for _, p := range policies {
		if nameFilter != "" && p.Name != nameFilter {
			continue
		}
		out = append(out, map[string]interface{}{"id": p.ID, "name": p.Name})
		names = append(names, p.Name)
	}

	if err := d.Set("policies", out); err != nil {
		return diag.FromErr(err)
	}
	if err := d.Set("names", names); err != nil {
		return diag.FromErr(err)
	}

	sum := sha256.Sum256([]byte(strings.Join(names, ",")))
	d.SetId(fmt.Sprintf("%x", sum[:8]))

	return nil
}
