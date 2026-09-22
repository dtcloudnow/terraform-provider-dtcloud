// Package router implements the dtcloud_router resource, the two resources that
// hang off it, and their data sources.
//
// Three API facts shape the package: a router is always created with an external
// gateway, so external_network_id is Required; interfaces and static routes are
// separate resources, because the create endpoint's own way of attaching one
// deletes the whole router if it fails; and a static route is applied by
// rewriting the router's entire route list, so the routes of one router are
// serialised on its id.
package router

import (
	"context"
	"fmt"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/wait"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// noHTMLPattern mirrors the character rule on a router name, so a request
// rejected halfway through an apply becomes an error during plan.
const noHTMLPattern = `^[^<>&"']*$`

// externalInterfaceType and internalInterfaceType label the two kinds of entry
// in the interface listing. The `id` field means a subnet id on the first and a
// port id on the second.
const (
	externalInterfaceType = "External gateway"
	internalInterfaceType = "Internal interface"
)

// routerSettled reports whether a status means the platform has finished.
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
// requested change has landed. Both are needed: a write is acknowledged before
// it is applied and the router stays ACTIVE throughout, so a status-only wait
// would return having checked nothing. A nil `settled` accepts any resting state.
func waitForRouter(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration, settled func(*dtgo.GetRouterDetails) bool) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.Router.GetRouterDetails(ctx, id, nil)
			if err != nil {
				// A router that has not appeared yet is not a failure.
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
			// Every other non-target status counts as pending, so an unfamiliar one is
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
		Delay:      wait.Pace(2 * time.Second),
		MinTimeout: wait.Pace(3 * time.Second),
		// Two readings in a row, so a poll landing between a request being accepted
		// and the router leaving ACTIVE cannot end the wait.
		ContinuousTargetOccurence: 2,
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// waitForRouterGone blocks until the router stops resolving, so destroy does not
// return while the public address it holds is still charged.
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
		Delay:      wait.Pace(2 * time.Second),
		MinTimeout: wait.Pace(3 * time.Second),
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
		Delay:      wait.Pace(2 * time.Second),
		MinTimeout: wait.Pace(3 * time.Second),
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// externalGatewayParams builds the gateway half of an update. The fixed IP is
// sent as a single IPv4 entry, matching what create hardcodes: repeating it
// keeps an update from quietly changing the address, and keeps the field off
// `null`, which the endpoint rejects.
func externalGatewayParams(networkID string, enableSnat bool) dtgo.AttachExternalGatewayToRouterParams {
	var params dtgo.AttachExternalGatewayToRouterParams
	params.ExternalGatewayInfo.NetworkId = networkID
	params.ExternalGatewayInfo.EnableSnat = enableSnat
	params.ExternalGatewayInfo.ExternalFixedIPs = []dtgo.FixedIP{{IPVersion: 4}}
	return params
}

// firstExternalSubnetID is the subnet the gateway's first address sits on as
// state has it, or "" when there is none. Used to tell a moved gateway apart
// from one that has not moved yet.
func firstExternalSubnetID(d *schema.ResourceData) string {
	ips, ok := d.Get("external_fixed_ip").([]interface{})
	if !ok || len(ips) == 0 {
		return ""
	}
	first, ok := ips[0].(map[string]interface{})
	if !ok {
		return ""
	}
	subnetID, _ := first["subnet_id"].(string)
	return subnetID
}

// flattenExternalFixedIPs turns the gateway's addresses into state. There is no
// way to request particular ones, so they are read-only.
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
// so a change made outside Terraform shows up as drift. An empty external
// network id means the gateway was removed elsewhere and is written through as
// such, because that is exactly the difference a plan should show.
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
