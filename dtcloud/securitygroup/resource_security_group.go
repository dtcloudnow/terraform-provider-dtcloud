package securitygroup

import (
	"context"
	"fmt"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudSecurityGroup manages a security group.
//
// The group itself is only a name and a description; the rules inside it are
// dtcloud_security_group_rule resources. Both arguments change in place — there
// is a PUT — so nothing here is ForceNew, which matters: destroying a security
// group that instances reference is refused by the platform, and a resource
// that rebuilt itself over a rename would deadlock against that.
//
// `inbound_rule` and `outbound_rule` are a read-only snapshot of what the
// platform displays, not something to configure. See the package comment for
// why they cannot be anything else.
func ResourceDtcloudSecurityGroup() *schema.Resource {
	return &schema.Resource{
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
				Description: "Description of the security group. Can be changed in place, but see the note on " +
					"clearing it: an existing description cannot be emptied.",
			},

			"inbound_rule":  cookedRuleSchema("Inbound rules as the platform displays them. Read-only; manage rules with dtcloud_security_group_rule."),
			"outbound_rule": cookedRuleSchema("Outbound rules as the platform displays them. A new group starts with two, added by the platform: allow-all IPv4 and IPv6 egress."),
		},

		// The one rule the schema cannot express, and it exists because of an
		// SDK limitation rather than an API one — see Update.
		CustomizeDiff: func(ctx context.Context, d *schema.ResourceDiff, meta interface{}) error {
			if d.Id() == "" {
				return nil
			}
			old, new := d.GetChange("description")
			if old.(string) != "" && new.(string) == "" {
				return fmt.Errorf(
					"description cannot be cleared once set: dt-go marks the field `omitempty`, so an empty " +
						"description is dropped from the update request and the old one stays. Leaving this to be " +
						"discovered at apply time would produce a plan that never converges. Set a new description, " +
						"or recreate the group with `terraform taint`")
			}
			return nil
		},

		// Every call in this package is synchronous — no socketUtils, no status
		// to poll. These bound a hung request rather than a slow rollout.
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
	// Some endpoints answer 200 with a hollow object rather than a 404; an id
	// that did not come back means the group is gone either way.
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

	// `name` goes every time, not only when it changed: the update route
	// validates with Joi and marks name required, so a description-only edit
	// sent on its own is refused with a 406.
	params := dtgo.UpdateSecurityGroupParams{
		Name:        d.Get("name").(string),
		Description: d.Get("description").(string),
	}

	if _, _, err := client.SecurityGroup.UpdateSecurityGroup(ctx, d.Id(), params, nil); err != nil {
		return diag.Errorf("Error updating security group %q: %s", d.Id(), err)
	}

	return resourceDtcloudSecurityGroupRead(ctx, d, meta)
}

func resourceDtcloudSecurityGroupDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	// A group still bound to a port is refused by Neutron, which is correct and
	// should surface. Terraform orders the destroy itself when the VM's
	// `security_groups` references this resource; a group referenced by a
	// hand-written id has no such edge and will fail here until the instance
	// using it is gone.
	if _, err := client.SecurityGroup.DeleteSecurityGroup(ctx, d.Id(), nil); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error deleting security group %q: %s", d.Id(), err)
		}
	}

	d.SetId("")
	return nil
}
