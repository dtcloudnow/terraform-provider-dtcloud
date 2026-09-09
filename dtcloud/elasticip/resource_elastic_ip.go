package elasticip

import (
	"context"
	"fmt"
	"time"

	dtgo "github.com/dtcloudnow/dt-go"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudElasticIP allocates a public address and points it at a port.
//
// The three verbs of the service map onto Terraform's model directly: create
// allocates, update associates and disassociates, delete releases. Moving the
// address between machines is an in-place update — the address itself, which is
// the part worth keeping, is never released to change what it reaches.
//
// `port_id` is the association, and the only argument that writes it. See the
// package comment for why there is no separate association resource.
func ResourceDtcloudElasticIP() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceDtcloudElasticIPCreate,
		ReadContext:   resourceDtcloudElasticIPRead,
		UpdateContext: resourceDtcloudElasticIPUpdate,
		DeleteContext: resourceDtcloudElasticIPDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},

		Schema: map[string]*schema.Schema{
			"floating_network_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description: "ID of the external network to allocate the address from. " +
					"`GET /openstack/routers/externals` reports which external network each of your networks is routed to.",
			},
			"subnet_id": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				// The API accepts this on create and never reports it back, so
				// an imported address has nothing here. Without the suppression
				// below, adding the resource to a configuration that names a
				// subnet would propose destroying and re-allocating the address
				// — losing the address — to satisfy a difference that is not
				// real. Empty state is absence of evidence, not evidence of a
				// difference.
				DiffSuppressFunc: func(k, old, new string, d *schema.ResourceData) bool {
					return old == ""
				},
				Description: "ID of a specific subnet of the external network to take the address from. " +
					"Only used when allocating, and never reported back — see the note on import.",
			},

			"port_id": {
				Type:     schema.TypeString,
				Optional: true,
				Description: "ID of the port to point the address at. Remove it to disassociate. " +
					"A VM's ports are on its `network_interface` blocks: " +
					"`dtcloud_vm.web.network_interface[0].port_id`.",
			},
			"fixed_ip_address": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ValidateFunc: validation.IsIPAddress,
				Description: "Which address on the port to map to, for a port that has several. " +
					"The platform picks one when omitted.",
			},

			"ip_address": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The public address that was allocated. This is the value to hand out.",
			},
			"status": {
				Type:     schema.TypeString,
				Computed: true,
				Description: "`ACTIVE` when the address is associated, `DOWN` when it is not. " +
					"`DOWN` is a resting state, not a fault.",
			},
			"device_id": {
				Type:     schema.TypeString,
				Computed: true,
				Description: "ID of the resource behind the port — the virtual machine or load balancer this " +
					"address currently reaches. Empty when the address is not associated.",
			},
			"device_owner": {
				Type:     schema.TypeString,
				Computed: true,
				Description: "What kind of thing is behind the port, as the platform labels it, " +
					"e.g. `compute:nova` for a VM. Empty when the address is not associated.",
			},
			"router_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "ID of the router carrying the traffic, filled in once the address is associated.",
			},
			"description": {
				Type:     schema.TypeString,
				Computed: true,
				Description: "Description of the address. Read-only: neither the create nor the update route " +
					"accepts one, so this is only ever set for an address created outside Terraform.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "When the address was allocated, in RFC 3339 format.",
			},
			"updated_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "When the address was last changed, in RFC 3339 format.",
			},
		},

		CustomizeDiff: func(ctx context.Context, d *schema.ResourceDiff, meta interface{}) error {
			// The rule the schema cannot express: a fixed address only means
			// something relative to a port. Left to the API it is accepted and
			// quietly ignored, which is worse than an error.
			if d.Get("fixed_ip_address").(string) != "" && d.Get("port_id").(string) == "" {
				// Computed: after a disassociate the platform's old value can
				// still be in state with no port. Only a value the practitioner
				// actually wrote is worth refusing.
				if raw := d.GetRawConfig(); !raw.IsNull() {
					v := raw.GetAttr("fixed_ip_address")
					if !v.IsNull() {
						return fmt.Errorf("fixed_ip_address needs port_id: it selects which address on a port to " +
							"map to, so on its own there is nothing for it to select from and the platform ignores it")
					}
				}
			}

			// Everything below is derived from the association, so changing the
			// port changes all of it — and Terraform has to be told that during
			// the plan, not after.
			//
			// Without this, the plan carries the *prior* values for these
			// attributes, Read afterwards returns the new ones, and Terraform
			// treats that as the provider contradicting its own plan. Under
			// SDKv2 that is not an error: the legacy type system downgrades it
			// to a log line and keeps the planned values. State then holds the
			// previous association until something unrelated triggers a refresh
			// — the resource silently reports the machine it used to point at.
			//
			// Confirmed on DEV: `terraform apply` of a disassociate left status
			// ACTIVE and device_id still set, while the API had already
			// detached, and TF_LOG showed ".status: was cty.StringVal(\"ACTIVE\")"
			// under "legacy plugin SDK".
			if d.Id() != "" && d.HasChange("port_id") {
				for _, key := range []string{"status", "device_id", "device_owner", "router_id"} {
					if err := d.SetNewComputed(key); err != nil {
						return err
					}
				}
				// Only when the practitioner has not pinned it; a configured
				// value is theirs and is not the platform's to choose.
				if raw := d.GetRawConfig(); raw.IsNull() || raw.GetAttr("fixed_ip_address").IsNull() {
					if err := d.SetNewComputed("fixed_ip_address"); err != nil {
						return err
					}
				}
			}
			return nil
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(10 * time.Minute),
			Update: schema.DefaultTimeout(10 * time.Minute),
			Delete: schema.DefaultTimeout(10 * time.Minute),
		},
	}
}

// configuredFixedIP returns fixed_ip_address only when the configuration
// actually names one.
//
// `d.Get` cannot be used for this. The field is Optional+Computed, so once the
// platform has filled it in, `d.Get` keeps returning that value even though the
// practitioner never asked for it — and re-sending it is wrong the moment the
// address moves to a different port, because an address that belonged to the
// old port is not one the new port has. The platform rejects the pairing and
// the move fails.
//
// So the rule is: send it only if it was written down. Anything else, let the
// platform choose and let Computed absorb the answer.
func configuredFixedIP(d *schema.ResourceData) string {
	raw := d.GetRawConfig()
	if raw.IsNull() || !raw.IsKnown() {
		return ""
	}
	v := raw.GetAttr("fixed_ip_address")
	if v.IsNull() || !v.IsKnown() {
		return ""
	}
	return v.AsString()
}

func resourceDtcloudElasticIPCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	params := dtgo.CreateFloatingIpParams{
		FloatingNetworkID: d.Get("floating_network_id").(string),
		PortID:            d.Get("port_id").(string),
		SubnetID:          d.Get("subnet_id").(string),
	}
	// Only meaningful alongside a port, and CustomizeDiff has already refused
	// the combination the practitioner could have got wrong.
	if params.PortID != "" {
		params.FixedIPAddress = configuredFixedIP(d)
	}

	created, body, err := client.FloatingIps.Create(ctx, params, nil)
	if err != nil {
		return diag.Errorf("Error allocating elastic IP: %s", err)
	}
	if created == nil || created.ID == "" {
		return diag.Errorf("The API accepted the allocation but returned no id (body: %s)", body)
	}

	d.SetId(created.ID)

	if err := waitForAssociation(ctx, client, created.ID, params.PortID, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.Errorf("Error waiting for elastic IP %q to settle: %s", created.ID, err)
	}

	return resourceDtcloudElasticIPRead(ctx, d, meta)
}

func resourceDtcloudElasticIPRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	details, body, err := client.FloatingIps.GetDetails(ctx, d.Id(), nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error retrieving elastic IP %q: %s", d.Id(), err)
	}
	if details == nil || details.ID == "" {
		d.SetId("")
		return nil
	}

	d.Set("floating_network_id", details.FloatingNetworkID)
	d.Set("ip_address", details.FloatingIPAddress)
	d.Set("status", details.Status)
	d.Set("port_id", deref(details.PortID))
	d.Set("fixed_ip_address", deref(details.FixedIPAddress))
	d.Set("router_id", deref(details.RouterID))
	d.Set("description", details.Description)
	d.Set("created_at", formatTime(details.CreatedAt))
	d.Set("updated_at", formatTime(details.UpdatedAt))

	// Read from the raw body: dt-go types this field as a string and the API
	// sends an object. See parsePortDetails.
	pd := parsePortDetails(body)
	d.Set("device_id", pd.DeviceID)
	d.Set("device_owner", pd.DeviceOwner)

	return nil
}

func resourceDtcloudElasticIPUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	if !d.HasChanges("port_id", "fixed_ip_address") {
		return resourceDtcloudElasticIPRead(ctx, d, meta)
	}

	portID := d.Get("port_id").(string)

	// The whole association goes every time, because the endpoint has no
	// partial mode: a body without port_id is how the platform is told to
	// disassociate, not how it is told to leave things alone. dt-go's omitempty
	// on both fields is what makes that reachable — an empty PortID serialises
	// the body away to `{}`, which is exactly the disassociate call.
	params := dtgo.UpdateFloatingIpParams{PortID: portID}
	if portID != "" {
		params.FixedIPAddress = configuredFixedIP(d)
	}

	if _, _, err := client.FloatingIps.Update(ctx, d.Id(), params, nil); err != nil {
		verb := "associating"
		if portID == "" {
			verb = "disassociating"
		}
		return diag.Errorf("Error %s elastic IP %q: %s", verb, d.Id(), err)
	}

	if err := waitForAssociation(ctx, client, d.Id(), portID, d.Timeout(schema.TimeoutUpdate)); err != nil {
		return diag.Errorf("Error waiting for elastic IP %q to reach the requested association: %s", d.Id(), err)
	}

	return resourceDtcloudElasticIPRead(ctx, d, meta)
}

func resourceDtcloudElasticIPDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	if _, err := client.FloatingIps.Delete(ctx, d.Id(), nil); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error releasing elastic IP %q: %s", d.Id(), err)
		}
	}

	if err := waitForGone(ctx, client, d.Id(), d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.Errorf("Error waiting for elastic IP %q to be released: %s", d.Id(), err)
	}

	d.SetId("")
	return nil
}
