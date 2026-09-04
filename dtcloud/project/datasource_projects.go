// Package project exposes the projects on the account and their resource usage.
//
// Read-only. The API also has `POST /openstack/projects/{id}`, which switches
// the *caller's* active project — session state rather than infrastructure, and
// driving it from a resource would silently change which project every other
// call lands in. It is deliberately not exposed.
package project

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudProjects lists the projects on the account.
func DataSourceDtcloudProjects() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceDtcloudProjectsRead,
		Schema: map[string]*schema.Schema{
			"projects": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The projects visible to the caller.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":        {Type: schema.TypeString, Computed: true},
						"name":      {Type: schema.TypeString, Computed: true},
						"domain_id": {Type: schema.TypeString, Computed: true},
					},
				},
			},
			"active_project_id": {
				Type:     schema.TypeString,
				Computed: true,
				Description: "The project the caller's session is currently pointed at. Everything " +
					"this provider creates lands in it.",
			},
			"active_project_name": {Type: schema.TypeString, Computed: true, Description: "Name of that project."},
			"active_region_id":    {Type: schema.TypeInt, Computed: true, Description: "The region the caller's session is currently pointed at."},
		},
	}
}

func dataSourceDtcloudProjectsRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	resp, _, err := client.Project.ListProjects(ctx, nil)
	if err != nil {
		return diag.Errorf("Error listing projects: %s", err)
	}

	out := make([]interface{}, 0, len(resp.Projects))
	ids := make([]string, 0, len(resp.Projects))
	for _, p := range resp.Projects {
		out = append(out, map[string]interface{}{
			"id": p.ID, "name": p.Name, "domain_id": p.DomainID,
		})
		ids = append(ids, p.ID)
	}

	if err := d.Set("projects", out); err != nil {
		return diag.FromErr(err)
	}
	d.Set("active_project_id", resp.LastProjectID)
	d.Set("active_project_name", resp.LastProject)
	d.Set("active_region_id", resp.LastServerID)

	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	d.SetId(fmt.Sprintf("%x", sum[:8]))

	return nil
}
