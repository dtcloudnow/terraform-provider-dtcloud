package router

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	dtgo "github.com/dtcloudnow/dt-go"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudRouter manages a virtual router.
//
// Everything a router carries can be changed in place: the name and the
// external gateway are the only arguments, and both have an update endpoint.
// The two are separate requests, because the API takes the name and the gateway
// through different bodies.
//
// Interfaces and static routes are not arguments here. They are
// dtcloud_router_interface and dtcloud_router_static_route, so that adding one
// does not rewrite the router, and so that a failure attaching one does not
// take the router with it.
func ResourceDtcloudRouter() *schema.Resource {
	return &schema.Resource{
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
			// Required because the platform has no router without a gateway:
			// the create endpoint takes the external network and the SNAT
			// setting as mandatory fields and always attaches a gateway.
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

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(10 * time.Minute),
			Update: schema.DefaultTimeout(10 * time.Minute),
			Delete: schema.DefaultTimeout(10 * time.Minute),
		},
	}
}

// mergeSchemas joins the argument half of a resource with the shared read-only
// half. Later maps win, which never happens here — the two halves are disjoint.
func mergeSchemas(schemas ...map[string]*schema.Schema) map[string]*schema.Schema {
	out := map[string]*schema.Schema{}
	for _, s := range schemas {
		for k, v := range s {
			out[k] = v
		}
	}
	return out
}

// createdRouterID reads the new router's id out of a create response. The typed
// value is used when present and the raw body parsed as a fallback, because a
// resource that starts life with an empty id is one Terraform will create a
// second time on the next apply.
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

	// The create call answers as soon as the request is accepted and the
	// platform then polls for ACTIVE over a websocket the provider has no part
	// in. Without this wait, a router would be handed to whatever depends on it
	// before it can carry traffic.
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

	// The name and the gateway travel in different request bodies, so changing
	// both is two requests rather than one.
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

	// Wait on the values that were asked for, not on the status. The router
	// stays ACTIVE across an update, so a status-only wait would return without
	// having established anything.
	wantName := d.Get("name").(string)
	wantNetwork := d.Get("external_network_id").(string)
	wantSnat := d.Get("enable_snat").(bool)
	settled := func(details *dtgo.GetRouterDetails) bool {
		return details.Name == wantName &&
			details.ExternalGatewayInfo.NetworkID == wantNetwork &&
			details.ExternalGatewayInfo.EnableSnat == wantSnat
	}
	if err := waitForRouter(ctx, client, id, d.Timeout(schema.TimeoutUpdate), settled); err != nil {
		return diag.Errorf("Error waiting for the update of router %q to be applied: %s", id, err)
	}

	return resourceDtcloudRouterRead(ctx, d, meta)
}

func resourceDtcloudRouterDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	id := d.Id()

	// Deleting a router detaches its interfaces first, on the platform's side.
	// Terraform's own graph already destroys dtcloud_router_interface resources
	// before the router they belong to, so this only matters for interfaces
	// attached outside Terraform.
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
