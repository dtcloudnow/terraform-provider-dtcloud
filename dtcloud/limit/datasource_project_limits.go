// Package limit exposes a project's raw OpenStack quota table.
//
// Read-only, and the only endpoint the service has.
package limit

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// DataSourceDtcloudProjectLimits reports every quota the platform tracks for a
// project — limits only; `dtcloud_project_quotas` has usage alongside.
//
// The keys are the platform's and it can add more, so they arrive as a map:
// `data.dtcloud_project_limits.mine.quotas["cores"]`.
func DataSourceDtcloudProjectLimits() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceDtcloudProjectLimitsRead,
		Schema: map[string]*schema.Schema{
			"project_id": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the project, from dtcloud_projects.",
			},
			"quotas": {
				Type:     schema.TypeMap,
				Computed: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
				Description: "Every quota the platform reports, keyed by its OpenStack name. " +
					"Values are numbers as text, or the word \"Unlimited\" where the platform " +
					"reports no limit. Terraform maps hold one type, and the API mixes numbers " +
					"with that word, so everything is rendered as text — use `tonumber()` when " +
					"you need arithmetic.",
			},
			"names": {
				Type:        schema.TypeList,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "The quota names, sorted. Useful for discovering what the platform tracks.",
			},
		},
	}
}

func dataSourceDtcloudProjectLimitsRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	projectID := d.Get("project_id").(string)

	limits, _, err := client.Limit.ListProjectQuotas(ctx, projectID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("Project %q not found", projectID)
		}
		return diag.Errorf("Error retrieving quotas for project %q: %s", projectID, err)
	}
	if limits == nil {
		return diag.Errorf("The API returned no quota data for project %q", projectID)
	}

	quotas := make(map[string]interface{}, len(limits.Quotas))
	names := make([]interface{}, 0, len(limits.Quotas))
	sorted := make([]string, 0, len(limits.Quotas))
	for name := range limits.Quotas {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	for _, name := range sorted {
		quotas[name] = formatQuota(limits.Quotas[name])
		names = append(names, name)
	}

	if err := d.Set("quotas", quotas); err != nil {
		return diag.FromErr(err)
	}
	if err := d.Set("names", names); err != nil {
		return diag.FromErr(err)
	}

	id := limits.ID
	if id == "" {
		id = projectID
	}
	d.SetId(id)

	return nil
}

// formatQuota renders a quota value as text. dt-go hands back a float64 or the
// string "Unlimited", which it substitutes for the API's -1. Whole numbers
// print without a decimal point, so `cores` reads as "48".
func formatQuota(v interface{}) string {
	switch n := v.(type) {
	case string:
		return n
	case float64:
		if n == math.Trunc(n) && math.Abs(n) < 1e15 {
			return fmt.Sprintf("%d", int64(n))
		}
		return fmt.Sprintf("%g", n)
	default:
		return fmt.Sprint(v)
	}
}
