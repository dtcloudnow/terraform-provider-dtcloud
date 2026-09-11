// Package region exposes the regions available to the account.
//
// Read-only. The API also has `POST /openstack/regions`, which switches the
// *caller's* active region — that is session state, not infrastructure, and the
// provider already takes a region through its own `region_id` argument.
// Driving it from a resource would silently change which region every other
// call lands in, so it is deliberately not exposed.
package region

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// regionMetadata is the part of the response dt-go's typed struct leaves out.
//
// `ListRegionsResponse` carries only id and name, but the API also reports
// which services a region actually offers — worth having, since a configuration
// that builds a load balancer in a region without them fails at apply time
// rather than at plan time. Every dt-go method also returns the raw body, so it
// is decoded here rather than changing the SDK.
type regionMetadata struct {
	Regions []struct {
		ID       int    `json:"id"`
		Name     string `json:"name"`
		Metadata struct {
			ServiceAvailability map[string]bool `json:"serviceAvailability"`
		} `json:"metadata"`
	} `json:"regions"`
}

// DataSourceDtcloudRegions lists the regions available to the account.
func DataSourceDtcloudRegions() *schema.Resource {
	return &schema.Resource{
		Description: "Lists the regions available to the account.\n\n" +
			"The region a resource is created in comes from the provider's `region_id`, not from here; " +
			"this is how to discover the valid values.",

		ReadContext: dataSourceDtcloudRegionsRead,
		Schema: map[string]*schema.Schema{
			"regions": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The regions visible to the caller.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id": {
							Type:        schema.TypeInt,
							Computed:    true,
							Description: "Numeric region id. This is what the provider's region_id argument takes.",
						},
						"name": {Type: schema.TypeString, Computed: true},
						"available_services": {
							Type:     schema.TypeList,
							Computed: true,
							Elem:     &schema.Schema{Type: schema.TypeString},
							Description: "Services this region offers, sorted. Empty when the platform " +
								"reports no availability information for the region.",
						},
					},
				},
			},
			"ids": {
				Type:        schema.TypeList,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeInt},
				Description: "Just the ids.",
			},
		},
	}
}

func dataSourceDtcloudRegionsRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	regions, body, err := client.Region.ListRegions(ctx)
	if err != nil {
		return diag.Errorf("Error listing regions: %s", err)
	}

	// The typed response is the source of truth for id and name; the raw body
	// only fills in what the type does not carry. If it cannot be decoded the
	// data source still works, just without the service list.
	services := map[int][]string{}
	var meta2 regionMetadata
	if err := json.Unmarshal([]byte(body), &meta2); err == nil {
		for _, r := range meta2.Regions {
			names := make([]string, 0, len(r.Metadata.ServiceAvailability))
			for name, available := range r.Metadata.ServiceAvailability {
				if available {
					names = append(names, name)
				}
			}
			sort.Strings(names)
			services[r.ID] = names
		}
	}

	out := make([]interface{}, 0, len(regions.Regions))
	ids := make([]interface{}, 0, len(regions.Regions))
	key := make([]string, 0, len(regions.Regions))
	for _, r := range regions.Regions {
		available := make([]interface{}, 0, len(services[r.ID]))
		for _, s := range services[r.ID] {
			available = append(available, s)
		}
		out = append(out, map[string]interface{}{
			"id": r.ID, "name": r.Name, "available_services": available,
		})
		ids = append(ids, r.ID)
		key = append(key, fmt.Sprint(r.ID))
	}

	if err := d.Set("regions", out); err != nil {
		return diag.FromErr(err)
	}
	if err := d.Set("ids", ids); err != nil {
		return diag.FromErr(err)
	}

	sum := sha256.Sum256([]byte(strings.Join(key, ",")))
	d.SetId(fmt.Sprintf("%x", sum[:8]))

	return nil
}
