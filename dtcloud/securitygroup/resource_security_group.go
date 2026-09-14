package securitygroup

import (
	"context"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudSecurityGroup manages a security group: a name and a
// description, with its rules as dtcloud_security_group_rule resources.
//
// Nothing here is ForceNew — the platform refuses to destroy a group instances
// reference, so rebuilding over a rename would deadlock. `inbound_rule` and
// `outbound_rule` are a read-only display snapshot.
func ResourceDtcloudSecurityGroup() *schema.Resource {
	return &schema.Resource{
		Description: "Manages a security group -- the set of firewall rules applied to a machine's network interfaces.",

		CreateContext: resourceDtcloudSecurityGroupCreate,
		ReadContext:   resourceDtcloudSecurityGroupRead,
		UpdateContext: resourceDtcloudSecurityGroupUpdate,
		DeleteContext: resourceDtcloudSecurityGroupDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
				ValidateFunc: validation.All(
					validation.NoZeroValues,
					validation.StringMatch(nameCharset, "name "+nameCharsetMessage),
				),
				Description: "Name of the security group. Can be changed in place. Names are not required to be unique.",
			},
			"description": {
				Type:         schema.TypeString,
				Optional:     true,
				ValidateFunc: validation.StringMatch(nameCharset, "description "+nameCharsetMessage),
				Description:  "Description of the security group. Can be changed in place, and removing it clears it.",
			},

			"inbound_rule":  cookedRuleSchema("Inbound rules as the platform displays them. Read-only; manage rules with dtcloud_security_group_rule."),
			"outbound_rule": cookedRuleSchema("Outbound rules as the platform displays them. A new group starts with two, added by the platform: allow-all IPv4 and IPv6 egress."),
		},

		// Every call in this package is synchronous, so these bound a hung
		// request rather than a slow rollout.
		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(5 * time.Minute),
			Update: schema.DefaultTimeout(5 * time.Minute),
			Delete: schema.DefaultTimeout(5 * time.Minute),
		},
	}
}

func resourceDtcloudSecurityGroupCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	created, body, err := client.SecurityGroup.CreateSecurityGroup(ctx, dtgo.CreateSecurityGroupParams{
		Name:        d.Get("name").(string),
		Description: d.Get("description").(string),
	}, nil)
	if err != nil {
		return diag.Errorf("Error creating security group: %s", err)
	}
	if created.SecurityGroup.ID == "" {
		return diag.Errorf("The API accepted the security group but returned no id (body: %s)", body)
	}

	d.SetId(created.SecurityGroup.ID)

	return resourceDtcloudSecurityGroupRead(ctx, d, meta)
}

func resourceDtcloudSecurityGroupRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	details, _, err := client.SecurityGroup.GetSecurityGroupDetails(ctx, d.Id(), nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error retrieving security group %q: %s", d.Id(), err)
	}
	// Some endpoints answer 200 with a hollow object rather than a 404; an id that
	// did not come back means the group is gone either way.
	if details.ID == "" {
		d.SetId("")
		return nil
	}

	d.Set("name", details.Name)
	d.Set("description", details.Description)
	d.Set("inbound_rule", flattenCookedRules(details.InboundRules))
	d.Set("outbound_rule", flattenCookedRules(details.OutboundRules))

	return nil
}

func resourceDtcloudSecurityGroupUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	// `name` goes every time: the update route marks it required. Description is a
	// pointer so an empty string reaches the route, which is how it is cleared.
	description := d.Get("description").(string)
	params := dtgo.UpdateSecurityGroupParams{
		Name:        d.Get("name").(string),
		Description: &description,
	}

	if _, _, err := client.SecurityGroup.UpdateSecurityGroup(ctx, d.Id(), params, nil); err != nil {
		return diag.Errorf("Error updating security group %q: %s", d.Id(), err)
	}

	return resourceDtcloudSecurityGroupRead(ctx, d, meta)
}

func resourceDtcloudSecurityGroupDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	// A group still bound to a port is refused, and that refusal should surface.
	// Terraform orders the destroy itself when a VM references this resource; a
	// group referenced by a hand-written id has no such edge.
	if _, err := client.SecurityGroup.DeleteSecurityGroup(ctx, d.Id(), nil); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error deleting security group %q: %s", d.Id(), err)
		}
	}

	d.SetId("")
	return nil
}
