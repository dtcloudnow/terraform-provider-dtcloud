package securitygroup

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudSecurityGroups lists the security groups visible to the
// caller: id, name and description only. The `name` filter is applied here,
// since the route takes no query parameters. Use the singular data source for
// rules.
func DataSourceDtcloudSecurityGroups() *schema.Resource {
	return &schema.Resource{
		Description: "Lists the security groups visible to the caller.",

		ReadContext: dataSourceDtcloudSecurityGroupsRead,
		Schema: map[string]*schema.Schema{
			"name": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return security groups with this exact name. Names are not unique, so this can still match several.",
			},
			"security_groups": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The security groups that matched.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":          {Type: schema.TypeString, Computed: true, Description: "ID of the security group."},
						"name":        {Type: schema.TypeString, Computed: true, Description: "Name of the security group."},
						"description": {Type: schema.TypeString, Computed: true, Description: "Description of the security group."},
					},
				},
			},
			"ids": {
				Type:        schema.TypeList,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "Just the ids, for the common case of filling a VM interface's `security_groups`, which takes ids.",
			},
		},
	}
}

func dataSourceDtcloudSecurityGroupsRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	groups, _, err := client.SecurityGroup.ListSecurityGroups(ctx, nil)
	if err != nil {
		return diag.Errorf("Error listing security groups: %s", err)
	}

	nameFilter := d.Get("name").(string)

	out := make([]interface{}, 0, len(groups))
	ids := make([]interface{}, 0, len(groups))
	flat := make([]string, 0, len(groups))
	for _, g := range groups {
		if nameFilter != "" && g.Name != nameFilter {
			continue
		}
		out = append(out, map[string]interface{}{
			"id":          g.ID,
			"name":        g.Name,
			"description": g.Description,
		})
		ids = append(ids, g.ID)
		flat = append(flat, g.ID)
	}

	if err := d.Set("security_groups", out); err != nil {
		return diag.FromErr(err)
	}
	if err := d.Set("ids", ids); err != nil {
		return diag.FromErr(err)
	}

	// A stable id keeps an unchanged result from showing up as a diff.
	sum := sha256.Sum256([]byte(strings.Join(flat, ",")))
	d.SetId(fmt.Sprintf("%x", sum[:8]))

	return nil
}
