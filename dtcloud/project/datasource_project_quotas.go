package project

import (
	"context"
	"fmt"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// DataSourceDtcloudProjectQuotas reports headline figures with usage and quota
// side by side; `dtcloud_project_limits` has the full table. A quota of `-1`
// means unlimited and is reported as `-1` rather than translated.
func DataSourceDtcloudProjectQuotas() *schema.Resource {
	usage := func(desc string) *schema.Schema {
		return &schema.Schema{
			Type:        schema.TypeList,
			Computed:    true,
			Description: desc,
			Elem: &schema.Resource{
				Schema: map[string]*schema.Schema{
					"usage": {Type: schema.TypeFloat, Computed: true, Description: "In use now."},
					"quota": {Type: schema.TypeFloat, Computed: true, Description: "Allowed. -1 means unlimited."},
				},
			},
		}
	}

	return &schema.Resource{
		ReadContext: dataSourceDtcloudProjectQuotasRead,
		Schema: map[string]*schema.Schema{
			"project_id": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the project, from dtcloud_projects.",
			},
			"region_id": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Region to report on. Defaults to the provider's configured region.",
			},

			"cpu":          usage("vCPU cores."),
			"ram":          usage("Memory in GiB."),
			"floating_ips": usage("Floating IP addresses."),

			"storage_space": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "Storage usage per volume type.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name":  {Type: schema.TypeString, Computed: true, Description: "Storage policy, e.g. HI-IOPS."},
						"usage": {Type: schema.TypeFloat, Computed: true},
						"quota": {Type: schema.TypeFloat, Computed: true, Description: "-1 means unlimited."},
					},
				},
			},

			"vm_status": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The project's VMs counted by state.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"count":       {Type: schema.TypeInt, Computed: true, Description: "Total."},
						"running":     {Type: schema.TypeInt, Computed: true},
						"stopped":     {Type: schema.TypeInt, Computed: true},
						"error":       {Type: schema.TypeInt, Computed: true},
						"in_progress": {Type: schema.TypeInt, Computed: true, Description: "Any VM with a pending task, whatever its nominal status."},
					},
				},
			},
		},
	}
}

func dataSourceDtcloudProjectQuotasRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	conf := meta.(*config.CombinedConfig)
	client := conf.DTClient()

	projectID := d.Get("project_id").(string)
	regionID := d.Get("region_id").(string)
	if regionID == "" {
		// The endpoint takes the region in the path, so it has to be spelled
		// out even though every other call gets it from the client.
		regionID = client.ServerId
	}
	if regionID == "" {
		return diag.Errorf("No region to report on: set region_id here, or region_id / DTCLOUD_REGION_ID on the provider")
	}

	quotas, _, err := client.Project.GetProjectQuotas(ctx, regionID, projectID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("Project %q not found in region %s", projectID, regionID)
		}
		return diag.Errorf("Error retrieving quotas for project %q: %s", projectID, err)
	}
	if quotas == nil {
		return diag.Errorf("The API returned no quota data for project %q", projectID)
	}

	pair := func(usage, quota float64) []interface{} {
		return []interface{}{map[string]interface{}{"usage": usage, "quota": quota}}
	}

	data := quotas.Data
	d.Set("cpu", pair(data.CPU.Usage, data.CPU.Quota))
	d.Set("ram", pair(data.RAM.Usage, data.RAM.Quota))
	d.Set("floating_ips", pair(data.FloatingIPs.Usage, data.FloatingIPs.Quota))

	storage := make([]interface{}, 0, len(data.StorageSpace))
	for _, s := range data.StorageSpace {
		storage = append(storage, map[string]interface{}{
			"name": s.Name, "usage": s.Usage, "quota": s.Quota,
		})
	}
	d.Set("storage_space", storage)

	d.Set("vm_status", []interface{}{map[string]interface{}{
		"count":       data.VMStatus.Count,
		"running":     data.VMStatus.Running,
		"stopped":     data.VMStatus.Stopped,
		"error":       data.VMStatus.Error,
		"in_progress": data.VMStatus.InProgress,
	}})

	d.SetId(fmt.Sprintf("%s:%s", regionID, projectID))

	return nil
}
