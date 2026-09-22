package router

import (
	"context"
	"fmt"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudRouterStaticRoute adds one static route to a router.
//
// The id is `<router-id>:<destination>:<next-hop>`, which is also what a route
// is: the pair identifies it and there is nothing else to change, so both parts
// are ForceNew.
//
// A route is added by reading the router's whole route list, editing it and
// writing it back, so the routes of one router are serialised on its id — two
// applied at once would overwrite each other. The request is acknowledged before
// the route is visible, so create waits until it appears in the list.
func ResourceDtcloudRouterStaticRoute() *schema.Resource {
	return &schema.Resource{
		Description: "Adds one static route to a router: traffic for a destination is sent to a next hop instead of out of the gateway.",

		CreateContext: resourceDtcloudRouterStaticRouteCreate,
		ReadContext:   resourceDtcloudRouterStaticRouteRead,
		DeleteContext: resourceDtcloudRouterStaticRouteDelete,
		Importer: &schema.ResourceImporter{
			StateContext: resourceDtcloudRouterStaticRouteImport,
		},

		Schema: map[string]*schema.Schema{
			"router_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the router the route is added to.",
			},
			// The two ends are validated by opposite rules: a destination without a
			// prefix length is rejected, and a next hop with one is.
			"destination": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.IsCIDR,
				Description:  "Destination network in CIDR notation, for example `192.168.50.0/24`.",
			},
			"next_hop": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.IsIPv4Address,
				Description:  "Address traffic for the destination is sent to. IPv4, without a prefix length.",
			},
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(10 * time.Minute),
			Delete: schema.DefaultTimeout(10 * time.Minute),
		},
	}
}

func staticRouteID(routerID, destination, nextHop string) string {
	return fmt.Sprintf("%s:%s:%s", routerID, destination, nextHop)
}

// parseStaticRouteID splits an id back into its three parts. It cuts at the
// first and the last separator, because the middle part can contain them: a
// destination may be an IPv6 prefix.
func parseStaticRouteID(id string) (routerID, destination, nextHop string, err error) {
	routerID, rest, found := strings.Cut(id, ":")
	if !found {
		return "", "", "", fmt.Errorf("expected an id of the form <router-id>:<destination>:<next-hop>, got %q", id)
	}
	sep := strings.LastIndex(rest, ":")
	if sep < 0 {
		return "", "", "", fmt.Errorf("expected an id of the form <router-id>:<destination>:<next-hop>, got %q", id)
	}
	destination, nextHop = rest[:sep], rest[sep+1:]
	if routerID == "" || destination == "" || nextHop == "" {
		return "", "", "", fmt.Errorf("expected an id of the form <router-id>:<destination>:<next-hop>, got %q", id)
	}
	return routerID, destination, nextHop, nil
}

// staticRoutePresent reports whether the router currently carries the route.
func staticRoutePresent(routes dtgo.ListRouterStaticRoutes, destination, nextHop string) bool {
	for _, route := range routes {
		if route.DestinationSubnet == destination && route.NextHop == nextHop {
			return true
		}
	}
	return false
}

func resourceDtcloudRouterStaticRouteCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	conf := meta.(*config.CombinedConfig)
	client := conf.DTClient()

	routerID := d.Get("router_id").(string)
	destination := d.Get("destination").(string)
	nextHop := d.Get("next_hop").(string)

	// Held across the wait, not just the call: the endpoint rewrites the whole
	// list, so another route must not read it until this one is in.
	conf.Lock(routerID)
	defer conf.Unlock(routerID)

	params := dtgo.AddStaticRouteToRouterParams{Destination: destination, Nexthop: nextHop}
	if _, _, err := client.Router.AddStaticRouteToRouter(ctx, routerID, params, nil); err != nil {
		return diag.Errorf("Error adding route %s via %s to router %q: %s", destination, nextHop, routerID, err)
	}

	if err := waitForStaticRoute(ctx, client, routerID, destination, nextHop, true, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.Errorf("Error waiting for route %s via %s to appear on router %q: %s", destination, nextHop, routerID, err)
	}

	d.SetId(staticRouteID(routerID, destination, nextHop))

	return resourceDtcloudRouterStaticRouteRead(ctx, d, meta)
}

func resourceDtcloudRouterStaticRouteRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	routerID := d.Get("router_id").(string)
	destination := d.Get("destination").(string)
	nextHop := d.Get("next_hop").(string)

	routes, _, err := client.Router.ListRouterStaticRoutes(ctx, routerID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error listing the static routes of router %q: %s", routerID, err)
	}

	if !staticRoutePresent(routes, destination, nextHop) {
		// Removed outside Terraform, or overwritten by another write to the list.
		d.SetId("")
		return nil
	}

	d.Set("router_id", routerID)
	d.Set("destination", destination)
	d.Set("next_hop", nextHop)
	return nil
}

func resourceDtcloudRouterStaticRouteDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	conf := meta.(*config.CombinedConfig)
	client := conf.DTClient()

	routerID := d.Get("router_id").(string)
	destination := d.Get("destination").(string)
	nextHop := d.Get("next_hop").(string)

	conf.Lock(routerID)
	defer conf.Unlock(routerID)

	params := dtgo.AddStaticRouteToRouterParams{Destination: destination, Nexthop: nextHop}
	if _, _, err := client.Router.DeleteStaticRouteFromRouter(ctx, routerID, params, nil); err != nil {
		// The router being gone takes its routes with it.
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error removing route %s via %s from router %q: %s", destination, nextHop, routerID, err)
		}
		d.SetId("")
		return nil
	}

	if err := waitForStaticRoute(ctx, client, routerID, destination, nextHop, false, d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.Errorf("Error waiting for route %s via %s to be removed from router %q: %s", destination, nextHop, routerID, err)
	}

	d.SetId("")
	return nil
}

func resourceDtcloudRouterStaticRouteImport(ctx context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	routerID, destination, nextHop, err := parseStaticRouteID(d.Id())
	if err != nil {
		return nil, err
	}
	d.Set("router_id", routerID)
	d.Set("destination", destination)
	d.Set("next_hop", nextHop)
	d.SetId(staticRouteID(routerID, destination, nextHop))
	return []*schema.ResourceData{d}, nil
}

// waitForStaticRoute blocks until the route is present on the router, or absent
// when want is false. Reading the value back is what turns a write the platform
// accepted but did not apply into a reported failure instead of silent drift.
func waitForStaticRoute(ctx context.Context, client *dtgo.Client, routerID, destination, nextHop string, want bool, timeout time.Duration) error {
	return waitForCondition(ctx, timeout, func() (bool, error) {
		routes, _, err := client.Router.ListRouterStaticRoutes(ctx, routerID, nil)
		if err != nil {
			if dterr.IsNotFound(err) {
				// A router that is gone carries no routes, which settles a removal and can
				// never settle an addition.
				return !want, nil
			}
			return false, err
		}
		return staticRoutePresent(routes, destination, nextHop) == want, nil
	})
}
