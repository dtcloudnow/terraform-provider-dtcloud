package router

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudRouter manages a virtual router.
//
// Both arguments change in place, in two requests: the API takes the name and
// the gateway through different bodies. Interfaces and static routes are their
// own resources, so adding one does not rewrite the router and a failure
// attaching one does not take the router with it.
func ResourceDtcloudRouter() *schema.Resource {
	return &schema.Resource{
		Description: "Manages a virtual router: what gives private networks a way out to the internet, and a way to reach each other.",

		CreateContext: resourceDtcloudRouterCreate,
		ReadContext:   resourceDtcloudRouterRead,
		UpdateContext: resourceDtcloudRouterUpdate,
		DeleteContext: resourceDtcloudRouterDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},

		Schema: mergeSchemas(map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
				ValidateFunc: validation.StringMatch(
					regexp.MustCompile(noHTMLPattern),
					`must not contain <, >, &, ' or "`,
				),
				Description: "Name of the router. Can be changed in place.",
			},
			// Required because the platform has no router without a gateway.
			"external_network_id": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description: "ID of the external network the router reaches the outside through. " +
					"Every router has one. Can be changed in place.",
			},
			"enable_snat": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  true,
				Description: "Whether the gateway masquerades traffic from the private networks " +
					"behind it. Can be changed in place.",
			},
		}, routerAttributesSchema()),

		// Moving the gateway moves its address with it and the platform picks the
		// new one. Left alone, the plan carries the old address forward, so anything
		// reading external_fixed_ip sees the previous network's IP for a whole apply.
		CustomizeDiff: func(_ context.Context, d *schema.ResourceDiff, _ interface{}) error {
			if d.Id() != "" && d.HasChange("external_network_id") {
				return d.SetNewComputed("external_fixed_ip")
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

// mergeSchemas joins the argument half of a resource with the shared read-only
// half. Later maps win, which never happens here — the two are disjoint.
func mergeSchemas(schemas ...map[string]*schema.Schema) map[string]*schema.Schema {
	out := map[string]*schema.Schema{}
	for _, s := range schemas {
		for k, v := range s {
			out[k] = v
		}
	}
	return out
}

// createdRouterID reads the new router's id out of a create response, falling
// back to the raw body: a resource that starts life with an empty id is one
// Terraform creates a second time on the next apply.
func createdRouterID(typed string, body string) (string, error) {
	if typed != "" {
		return typed, nil
	}

	var bare struct {
		ID     string `json:"id"`
		Router struct {
			ID string `json:"id"`
		} `json:"router"`
	}
	if err := json.Unmarshal([]byte(body), &bare); err != nil {
		return "", fmt.Errorf("the API response was not a JSON object: %w (body: %s)", err, body)
	}
	for _, candidate := range []string{bare.Router.ID, bare.ID} {
		if candidate != "" {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no router id in the API response (body: %s)", body)
}

func resourceDtcloudRouterCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	params := dtgo.CreateRouterParams{
		Name:               d.Get("name").(string),
		ExternalNetworkId:  d.Get("external_network_id").(string),
		ExternalEnableSnat: d.Get("enable_snat").(bool),
	}

	created, body, err := client.Router.CreateRouter(ctx, params, nil)
	if err != nil {
		return diag.Errorf("Error creating router %q: %s", params.Name, err)
	}

	typed := ""
	if created != nil {
		typed = created.Router.ID
	}
	id, err := createdRouterID(typed, body)
	if err != nil {
		return diag.Errorf("Error creating router %q: %s", params.Name, err)
	}
	d.SetId(id)

	// The create call answers as soon as the request is accepted, so without this
	// wait a router would be handed to its dependants before it can carry traffic.
	if err := waitForRouter(ctx, client, id, d.Timeout(schema.TimeoutCreate), nil); err != nil {
		return diag.Errorf("Error waiting for router %q to become ACTIVE: %s", id, err)
	}

	return resourceDtcloudRouterRead(ctx, d, meta)
}

func resourceDtcloudRouterRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	details, _, err := client.Router.GetRouterDetails(ctx, d.Id(), nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error reading router %q: %s", d.Id(), err)
	}
	if details == nil || details.ID == "" {
		d.SetId("")
		return nil
	}

	setRouterAttributes(d, details)
	return nil
}

func resourceDtcloudRouterUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	id := d.Id()

	// Read before anything changes: the only way to tell the new gateway address
	// from the old is to know which subnet the old one sat on.
	networkChanged := d.HasChange("external_network_id")
	oldSubnetID := firstExternalSubnetID(d)

	// The name and the gateway travel in different bodies, so changing both is
	// two requests.
	if d.HasChange("name") {
		params := dtgo.UpdateRouterParams{Name: d.Get("name").(string)}
		if _, _, err := client.Router.UpdateRouter(ctx, id, params, nil); err != nil {
			return diag.Errorf("Error renaming router %q: %s", id, err)
		}
	}

	if d.HasChanges("external_network_id", "enable_snat") {
		params := externalGatewayParams(d.Get("external_network_id").(string), d.Get("enable_snat").(bool))
		if _, _, err := client.Router.AttachExternalGatewayToRouter(ctx, id, params, nil); err != nil {
			return diag.Errorf("Error updating the external gateway of router %q: %s", id, err)
		}
	}

	// Wait on the values that were asked for, not on the status: the router stays
	// ACTIVE across an update.
	wantName := d.Get("name").(string)
	wantNetwork := d.Get("external_network_id").(string)
	wantSnat := d.Get("enable_snat").(bool)
	settled := func(details *dtgo.GetRouterDetails) bool {
		if details.Name != wantName ||
			details.ExternalGatewayInfo.NetworkID != wantNetwork ||
			details.ExternalGatewayInfo.EnableSnat != wantSnat {
			return false
		}
		if !networkChanged {
			return true
		}
		// The platform reports the new network_id while the gateway still holds the
		// old address, so stopping here would write that stale address into state.
		ips := details.ExternalGatewayInfo.ExternalFixedIps
		return len(ips) > 0 && ips[0].SubnetID != oldSubnetID
	}
	if err := waitForRouter(ctx, client, id, d.Timeout(schema.TimeoutUpdate), settled); err != nil {
		return diag.Errorf("Error waiting for the update of router %q to be applied: %s", id, err)
	}

	return resourceDtcloudRouterRead(ctx, d, meta)
}

func resourceDtcloudRouterDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	id := d.Id()

	// Deleting a router detaches its interfaces first. Terraform's graph already
	// destroys them beforehand, so this only matters for ones attached outside it.
	if _, err := client.Router.DeleteRouter(ctx, id, nil); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error deleting router %q: %s", id, err)
		}
	}

	if err := waitForRouterGone(ctx, client, id, d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.Errorf("Error waiting for router %q to be deleted: %s", id, err)
	}

	d.SetId("")
	return nil
}
