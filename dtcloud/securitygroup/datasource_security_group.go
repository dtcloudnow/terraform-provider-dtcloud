package securitygroup

import (
	"context"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudSecurityGroup looks up one security group, by id or by name.
// Names are not unique, so one matching more than one group is an error.
func DataSourceDtcloudSecurityGroup() *schema.Resource {
	return &schema.Resource{
		Description: "Looks up one security group, by id or by name.",

		ReadContext: dataSourceDtcloudSecurityGroupRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ExactlyOneOf: []string{"id", "name"},
				Description:  "ID of the security group to look up. Give this or `name`.",
			},
			"name": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ExactlyOneOf: []string{"id", "name"},
				Description:  "Name of the security group to look up. Give this or `id`. Must match exactly one group.",
			},
			"description": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Description of the security group.",
			},

			"inbound_rule":  cookedRuleSchema("Inbound rules as the platform displays them."),
			"outbound_rule": cookedRuleSchema("Outbound rules as the platform displays them."),
		},
	}
}

func dataSourceDtcloudSecurityGroupRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	id := d.Get("id").(string)

	// A name has to be resolved first: only the list endpoint reports names
	// alongside ids, and the details endpoint takes an id.
	if id == "" {
		name := d.Get("name").(string)
		groups, _, err := client.SecurityGroup.ListSecurityGroups(ctx, nil)
		if err != nil {
			return diag.Errorf("Error listing security groups: %s", err)
		}
		matches := make([]string, 0, 1)
		for _, g := range groups {
			if g.Name == name {
				matches = append(matches, g.ID)
			}
		}
		switch len(matches) {
		case 0:
			return diag.Errorf("No security group named %q", name)
		case 1:
			id = matches[0]
		default:
			return diag.Errorf(
				"%d security groups are named %q; names are not unique on this platform, so look this one up by id instead",
				len(matches), name)
		}
	}

	details, _, err := client.SecurityGroup.GetSecurityGroupDetails(ctx, id, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("Security group %q not found", id)
		}
		return diag.Errorf("Error retrieving security group %q: %s", id, err)
	}
	if details.ID == "" {
		return diag.Errorf("Security group %q not found", id)
	}

	d.SetId(details.ID)
	d.Set("id", details.ID)
	d.Set("name", details.Name)
	d.Set("description", details.Description)
	d.Set("inbound_rule", flattenCookedRules(details.InboundRules))
	d.Set("outbound_rule", flattenCookedRules(details.OutboundRules))

	return nil
}
