// Package router implements the dtcloud_router resource, the two resources
// that hang off it, and their data sources.
//
// A router connects private networks to each other and to the outside world.
// Three things about the API shape everything in this package:
//
//   - A router is always created with an external gateway. The create endpoint
//     requires an external network and a SNAT setting, and there is no way to
//     ask for a router without one — so external_network_id is Required rather
//     than optional.
//
//   - Interfaces and static routes are separate resources, not arguments on the
//     router. Both have their own lifecycle, and the create endpoint's own way
//     of attaching interfaces deletes the whole router if any one of them
//     fails, which would leave Terraform holding a create error and no id.
//
//   - Static routes are applied by rewriting the router's entire route list.
//     Two of them applied at once therefore overwrite each other, so the ones
//     sharing a router are serialised on the router id. See
//     resource_router_static_route.go.
package router

import (
	"context"
	"fmt"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// noHTMLPattern mirrors the character rule the API applies to a router name.
// Enforcing it in the schema turns a request rejected halfway through an apply
// into an error during plan.
const noHTMLPattern = `^[^<>&"']*$`

// externalInterfaceType and internalInterfaceType are the labels the interface
// listing puts on its two kinds of entry. The distinction matters because the
// `id` field means something different in each: on the external gateway it is a
// subnet id, and on an internal interface it is the port id that detaching
// needs.
const (
	externalInterfaceType = "External gateway"
	internalInterfaceType = "Internal interface"
)

// routerSettled reports whether a status means the platform has finished.
// `ACTIVE` is the resting state the API itself polls for after every write.
func routerSettled(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "ACTIVE")
}

// routerFailed reports whether a status means the platform gave up.
func routerFailed(status string) bool {
	return strings.Contains(strings.ToLower(status), "error")
}

func formatTime(t dtgo.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// waitForRouter blocks until the router is at rest and `settled` agrees the
// requested change has landed.
//
// The second condition is the point of this function. A write is acknowledged
// before it has been applied, and the router is reported as ACTIVE throughout —
// so a wait that only watched the status would return immediately, having
// checked nothing. Callers pass the value they asked for.
//
// A nil `settled` means any resting state will do, which is what create wants.
func waitForRouter(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration, settled func(*dtgo.GetRouterDetails) bool) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.Router.GetRouterDetails(ctx, id, nil)
			if err != nil {
				// A router that has not appeared yet is not a failure: create
				// answers before the platform has committed it.
				if dterr.IsNotFound(err) {
					return "waiting", "waiting", nil
				}
				return nil, "", err
			}
			if details == nil || details.ID == "" {
				return "waiting", "waiting", nil
			}
			if routerFailed(details.Status) {
				return nil, "", fmt.Errorf("router %q entered %s state", id, details.Status)
			}
			// Every other non-target status counts as pending. Transitional
			// states are deliberately not enumerated, so an unfamiliar one is
			// waited out rather than reported as an error.
			if !routerSettled(details.Status) {
				return "waiting", "waiting", nil
			}
			if settled != nil && !settled(details) {
				return "waiting", "waiting", nil
			}
			return "done", "done", nil
		},
		Timeout:    timeout,
		Delay:      2 * time.Second,
		MinTimeout: 3 * time.Second,
		// Two readings in a row, so a poll landing in the gap between a request
		// being accepted and the router leaving ACTIVE cannot end the wait on
		// its own.
		ContinuousTargetOccurence: 2,
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// waitForRouterGone blocks until the router stops resolving, so destroy does
// not return while the platform is still tearing it down and the public address
// it holds is still charged against the quota.
func waitForRouterGone(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.Router.GetRouterDetails(ctx, id, nil)
			if err != nil {
				if dterr.IsNotFound(err) {
					return "done", "done", nil
				}
				return nil, "", err
			}
			if details == nil || details.ID == "" {
				return "done", "done", nil
			}
			if routerFailed(details.Status) {
				return nil, "", fmt.Errorf("router %q entered %s state while being deleted", id, details.Status)
			}
			return "waiting", "waiting", nil
		},
		Timeout:    timeout,
		Delay:      2 * time.Second,
		MinTimeout: 3 * time.Second,
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// waitForCondition polls until check reports true, and is the shape used by the
// waits that watch a list rather than a status.
func waitForCondition(ctx context.Context, timeout time.Duration, check func() (bool, error)) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			ok, err := check()
			if err != nil {
				return nil, "", err
			}
			if !ok {
				return "waiting", "waiting", nil
			}
			return "done", "done", nil
		},
		Timeout:    timeout,
		Delay:      2 * time.Second,
		MinTimeout: 3 * time.Second,
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// externalGatewayParams builds the gateway half of an update.
//
// external_fixed_ips is sent as a single entry asking for IPv4, which is what
// the create endpoint hardcodes. Repeating it keeps an update from being the
// one call that quietly changes which address the router holds, and it keeps
// the field off `null`, which the endpoint's array validation rejects.
func externalGatewayParams(networkID string, enableSnat bool) dtgo.AttachExternalGatewayToRouterParams {
	var params dtgo.AttachExternalGatewayToRouterParams
	params.ExternalGatewayInfo.NetworkId = networkID
	params.ExternalGatewayInfo.EnableSnat = enableSnat
	params.ExternalGatewayInfo.ExternalFixedIPs = []dtgo.FixedIP{{IPVersion: 4}}
	return params
}

// flattenExternalFixedIPs turns the addresses the platform gave the gateway
// into state. There is no way to request particular ones, so they are read-only.
func flattenExternalFixedIPs(details *dtgo.GetRouterDetails) []interface{} {
	out := make([]interface{}, 0, len(details.ExternalGatewayInfo.ExternalFixedIps))
	for _, ip := range details.ExternalGatewayInfo.ExternalFixedIps {
		out = append(out, map[string]interface{}{
			"subnet_id":  ip.SubnetID,
			"ip_address": ip.IPAddress,
		})
	}
	return out
}

// routerAttributesSchema is the read-only half of a router, shared by the
// resource and the singular data source so the two cannot drift apart.
func routerAttributesSchema() map[string]*schema.Schema {
	return map[string]*schema.Schema{
		"status": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "Status reported by the platform. `ACTIVE` is the resting state.",
		},
		"admin_state_up": {
			Type:        schema.TypeBool,
			Computed:    true,
			Description: "Whether the router is administratively enabled.",
		},
		"description": {
			Type:     schema.TypeString,
			Computed: true,
			Description: "Description held by the platform. There is no way to set one through " +
				"this API, so it is reported but not managed.",
		},
		"external_fixed_ip": {
			Type:     schema.TypeList,
			Computed: true,
			Description: "Addresses the external network allocated to the gateway. The create " +
				"endpoint always asks for IPv4 and never accepts a specific address, so these " +
				"are read-only.",
			Elem: &schema.Resource{
				Schema: map[string]*schema.Schema{
					"subnet_id":  {Type: schema.TypeString, Computed: true, Description: "Subnet the address came from."},
					"ip_address": {Type: schema.TypeString, Computed: true, Description: "Address allocated to the gateway."},
				},
			},
		},
		"project_id": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "ID of the project the router belongs to.",
		},
		"created_at": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "When the router was created, as the platform reports it.",
		},
		"updated_at": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "When the router last changed.",
		},
	}
}

// setRouterAttributes writes a router's fields into state, arguments included,
// so that a change made outside Terraform shows up as drift.
//
// An empty external network id means the gateway was removed elsewhere: the
// details endpoint sends `external_gateway_info: null`, which decodes to the
// zero value. It is written through as an empty string rather than left alone,
// because a router that has lost its gateway is exactly the difference a plan
// should show.
func setRouterAttributes(d *schema.ResourceData, details *dtgo.GetRouterDetails) {
	d.Set("name", details.Name)
	d.Set("external_network_id", details.ExternalGatewayInfo.NetworkID)
	d.Set("enable_snat", details.ExternalGatewayInfo.EnableSnat)
	d.Set("status", details.Status)
	d.Set("admin_state_up", details.AdminStateUp)
	d.Set("description", details.Description)
	d.Set("external_fixed_ip", flattenExternalFixedIPs(details))
	d.Set("project_id", details.ProjectID)
	d.Set("created_at", formatTime(details.CreatedAt))
	d.Set("updated_at", formatTime(details.UpdatedAt))
}
