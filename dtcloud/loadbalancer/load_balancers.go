// Package loadbalancer implements the dtcloud_lb family of resources.
//
// A load balancer owns listeners, a listener owns a pool, and a pool owns
// members and at most one health monitor. Each is a separate resource so that it
// has its own lifecycle. dtcloud_lb_balancing_pool builds the whole chain in one
// call for those who want the shortcut; the two styles should not be mixed on
// the same load balancer.
package loadbalancer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/wait"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// Statuses reported by the API: one title-case word folded from the
// provisioning and operating statuses, so the raw vocabulary never appears.
const (
	statusCreating    = "Creating"
	statusConfiguring = "Configuring"
	statusDeleting    = "Deleting"
	statusError       = "Error"
)

// settled reports whether a status means the platform has stopped working on
// the load balancer. The transitional set is enumerated rather than the settled
// one: "Disabled" is a healthy load balancer with no listener and "Unknown" is
// one with a pool but no monitor, so waiting for a single word would hang.
func settled(status string) bool {
	switch status {
	case statusCreating, statusConfiguring, statusDeleting:
		return false
	default:
		return true
	}
}

// createLBResponse is the create body, which comes back raw.
type createLBResponse struct {
	LoadBalancer struct {
		ID string `json:"id"`
	} `json:"loadbalancer"`
}

// createdID pulls an id out of a raw create response. The child endpoints do
// not all wrap their result the same way, so every known shape is tried.
func createdID(body string) (string, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return "", fmt.Errorf("response was not a JSON object: %w (body: %s)", err, body)
	}

	// Either {"id": "..."} or {"<thing>": {"id": "..."}}.
	if raw, ok := probe["id"]; ok {
		var id string
		if err := json.Unmarshal(raw, &id); err == nil && id != "" {
			return id, nil
		}
	}
	for _, raw := range probe {
		var nested struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &nested); err == nil && nested.ID != "" {
			return nested.ID, nil
		}
	}
	return "", fmt.Errorf("no id in the API response (body: %s)", body)
}

// compositeID joins the ids that address a nested object. It is also the
// `terraform import` format.
func compositeID(parts ...string) string { return strings.Join(parts, ":") }

// bareID is the last segment of a composite id. The children address each other
// by id, and a sibling's Terraform id carries its parents' in front of it --
// `<lb-id>:<listener-id>` -- while the endpoints want the bare UUID and refuse
// anything else. Accepting both is what stops the natural
// `listener_id = dtcloud_lb_listener.x.id` from reaching the platform malformed.
func bareID(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// splitID is the inverse. It insists on the exact number of parts so a mistyped
// import fails with a useful message instead of half-populating a resource.
func splitID(id string, want int, shape string) ([]string, error) {
	parts := strings.Split(id, ":")
	if len(parts) != want {
		return nil, fmt.Errorf("expected an id of the form %s, got %q", shape, id)
	}
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("expected an id of the form %s, got %q", shape, id)
		}
	}
	return parts, nil
}

// waitForLBActive blocks until the load balancer settles — see settled().
// Create, update and delete are all asynchronous, and anything that is not the
// target counts as pending, so an unlisted transitional status cannot fail it.
func waitForLBActive(ctx context.Context, client *dtgo.Client, lbID string, timeout time.Duration) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"pending"},
		Target:  []string{"target"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.LoadBalancer.GetLoadBalancerDetails(ctx, lbID, nil)
			if err != nil {
				if dterr.IsNotFound(err) {
					return nil, "", nil
				}
				return nil, "", err
			}
			if details.Status == statusError {
				return details, "", fmt.Errorf("load balancer %s entered %s state", lbID, statusError)
			}
			if settled(details.Status) {
				return details, "target", nil
			}
			return details, "pending", nil
		},
		Timeout:                   timeout,
		Delay:                     wait.Pace(5 * time.Second),
		MinTimeout:                wait.Pace(3 * time.Second),
		ContinuousTargetOccurence: 2,
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// waitForLBGone blocks until the load balancer stops resolving, so destroy does
// not return while it is still being torn down.
func waitForLBGone(ctx context.Context, client *dtgo.Client, lbID string, timeout time.Duration) error {
	return waitForCondition(ctx, timeout, func() (bool, error) {
		details, _, err := client.LoadBalancer.GetLoadBalancerDetails(ctx, lbID, nil)
		if err != nil {
			if dterr.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		return details.ID == "", nil
	})
}

// waitForCondition polls check until it reports done. The child objects have no
// status of their own; the only signal is whether they appear in the parent's
// list.
func waitForCondition(ctx context.Context, timeout time.Duration, check func() (bool, error)) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			done, err := check()
			if err != nil {
				return nil, "", err
			}
			if done {
				return "done", "done", nil
			}
			return "waiting", "waiting", nil
		},
		Timeout:    timeout,
		Delay:      wait.Pace(2 * time.Second),
		MinTimeout: wait.Pace(2 * time.Second),
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// lbNetworkSchema is the network block the API reports on a load balancer.
func lbNetworkSchema() *schema.Schema {
	return &schema.Schema{
		Type:        schema.TypeList,
		Computed:    true,
		Description: "The network the load balancer's VIP sits on.",
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				"id":   {Type: schema.TypeString, Computed: true},
				"name": {Type: schema.TypeString, Computed: true},
				"ip":   {Type: schema.TypeString, Computed: true},
			},
		},
	}
}

func flattenLBNetwork(n dtgo.LoadBalancerNetwork) []interface{} {
	if n.ID == "" && n.Name == "" && n.IP == "" {
		return []interface{}{}
	}
	return []interface{}{map[string]interface{}{"id": n.ID, "name": n.Name, "ip": n.IP}}
}

func expandStringList(raw []interface{}) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

// insertHeadersSchema is the set of headers a listener can inject, mapped from
// HTTP header names to snake_case in expand/flattenInsertHeaders. forceNew is
// threaded through because dtcloud_lb_balancing_pool has no update endpoint.
func insertHeadersSchema(forceNew bool) *schema.Schema {
	field := func() *schema.Schema {
		return &schema.Schema{Type: schema.TypeString, Optional: true, ForceNew: forceNew}
	}
	return &schema.Schema{
		Type:        schema.TypeList,
		Optional:    true,
		ForceNew:    forceNew,
		MaxItems:    1,
		Description: "Headers the listener injects into requests forwarded to members.",
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				"x_forwarded_for":         field(),
				"x_forwarded_port":        field(),
				"x_forwarded_proto":       field(),
				"x_ssl_client_cn":         field(),
				"x_ssl_client_dn":         field(),
				"x_ssl_client_has_cert":   field(),
				"x_ssl_client_verify":     field(),
				"x_ssl_client_not_after":  field(),
				"x_ssl_client_not_before": field(),
				"x_ssl_client_sha1":       field(),
				"x_ssl_issuer":            field(),
			},
		},
	}
}

func expandInsertHeaders(raw []interface{}) dtgo.InsertHeaders {
	if len(raw) == 0 || raw[0] == nil {
		return dtgo.InsertHeaders{}
	}
	m := raw[0].(map[string]interface{})
	return dtgo.InsertHeaders{
		XForwardedFor:       m["x_forwarded_for"].(string),
		XForwardedPort:      m["x_forwarded_port"].(string),
		XForwardedProto:     m["x_forwarded_proto"].(string),
		XSSLClientCN:        m["x_ssl_client_cn"].(string),
		XSSLClientDN:        m["x_ssl_client_dn"].(string),
		XSSLClientHasCert:   m["x_ssl_client_has_cert"].(string),
		XSSLClientVerify:    m["x_ssl_client_verify"].(string),
		XSSLClientNotAfter:  m["x_ssl_client_not_after"].(string),
		XSSLClientNotBefore: m["x_ssl_client_not_before"].(string),
		XSSLClientSHA1:      m["x_ssl_client_sha1"].(string),
		XSSLIssuer:          m["x_ssl_issuer"].(string),
	}
}

func flattenInsertHeaders(h dtgo.InsertHeaders) []interface{} {
	empty := dtgo.InsertHeaders{}
	if h == empty {
		return []interface{}{}
	}
	return []interface{}{map[string]interface{}{
		"x_forwarded_for":         h.XForwardedFor,
		"x_forwarded_port":        h.XForwardedPort,
		"x_forwarded_proto":       h.XForwardedProto,
		"x_ssl_client_cn":         h.XSSLClientCN,
		"x_ssl_client_dn":         h.XSSLClientDN,
		"x_ssl_client_has_cert":   h.XSSLClientHasCert,
		"x_ssl_client_verify":     h.XSSLClientVerify,
		"x_ssl_client_not_after":  h.XSSLClientNotAfter,
		"x_ssl_client_not_before": h.XSSLClientNotBefore,
		"x_ssl_client_sha1":       h.XSSLClientSHA1,
		"x_ssl_issuer":            h.XSSLIssuer,
	}}
}

func intPtr(v int) *int       { return &v }
func boolPtr(v bool) *bool    { return &v }
func strPtr(v string) *string { return &v }

// resolveFlavorID maps the reported flavor back to a flavor id, which is what
// the schema stores. The name alone is not enough: each size is offered twice,
// once with ha false and once with ha true, and that flag is how high
// availability is chosen — so "small" HA and "small" non-HA are different ids.
func resolveFlavorID(ctx context.Context, client *dtgo.Client, flavorName string, ha bool) string {
	if flavorName == "" {
		return ""
	}
	flavors, _, err := client.Flavor.ListLoadBalancerFlavors(ctx, nil)
	if err != nil {
		return ""
	}
	match := ""
	for _, f := range flavors {
		if f.Name != flavorName || f.HA != ha {
			continue
		}
		if match != "" {
			return "" // still ambiguous; leave the stored value alone
		}
		match = f.ID
	}
	return match
}

// resolveMemberComputeServerID finds which VM holds an address on the load
// balancer's network, so `terraform import` can recover compute_server_id.
// Returns "" on anything unexpected; the caller keeps whatever it had.
func resolveMemberComputeServerID(ctx context.Context, client *dtgo.Client, lbID, address string) string {
	if address == "" {
		return ""
	}
	details, _, err := client.LoadBalancer.GetLoadBalancerDetails(ctx, lbID, nil)
	if err != nil || details.Network.ID == "" {
		return ""
	}
	body, err := client.LoadBalancer.ListLoadBalancerVmsByNetwork(ctx, details.Network.ID, nil)
	if err != nil {
		return ""
	}
	var vms []lbCandidateVM
	if err := json.Unmarshal([]byte(body), &vms); err != nil {
		return ""
	}
	match := ""
	for _, vm := range vms {
		for _, ip := range vm.IPs {
			if ip != address {
				continue
			}
			if match != "" && match != vm.ID {
				return "" // two VMs claim the address
			}
			match = vm.ID
		}
	}
	return match
}

// mutateChild performs one change to a load balancer's children, serialised
// against everything else happening on the same load balancer.
//
// The platform allows exactly one change at a time and refuses the rest, while
// Terraform runs independent resources in parallel — which a health monitor and
// a member are. So: wait for the load balancer to settle, make the call, then
// retry for as long as it reports busy, which is safe because a refusal applied
// nothing.
func mutateChild(ctx context.Context, client *dtgo.Client, lbID string, timeout time.Duration, do func() error) error {
	if err := waitForLBActive(ctx, client, lbID, timeout); err != nil {
		return err
	}

	var lastBusy error
	err := waitForCondition(ctx, timeout, func() (bool, error) {
		if err := do(); err != nil {
			if dterr.IsBusy(err) {
				lastBusy = err
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	if err != nil && lastBusy != nil {
		return fmt.Errorf("%w (the load balancer stayed busy: %s)", err, lastBusy)
	}
	return err
}
