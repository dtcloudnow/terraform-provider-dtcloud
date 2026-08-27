// Package network implements the dtcloud_network resource and its data sources.
//
// One resource covers both a network and its subnet, because the API does not
// let them be managed apart: `POST /openstack/networks` creates the pair in a
// single call, the details endpoint reports them together, and there is no
// endpoint that creates a subnet on its own. A separate dtcloud_subnet resource
// would have nothing to call.
//
// The pivot is `ipam_enabled`. It is one flag doing two jobs, which is worth
// knowing before reading anything else here:
//
//   - it becomes the network's `port_security_enabled`, and
//   - it decides whether a subnet is created at all.
//
// So an IPAM-disabled network has no subnet, no CIDR, no gateway and no DHCP —
// the details endpoint simply omits the whole `subnets` object and the `ipam`
// field. Verified live on DEV.
package network

import (
	"context"
	"fmt"
	"time"

	dtgo "github.com/dtcloudnow/dt-go"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// allocationPoolSchema is the address range a subnet hands out from.
func allocationPoolSchema() *schema.Schema {
	return &schema.Schema{
		Type:        schema.TypeList,
		Optional:    true,
		Computed:    true,
		Description: "Address ranges the subnet allocates from. The platform picks a sensible default when omitted.",
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				"start": {Type: schema.TypeString, Required: true, Description: "First address in the range."},
				"end":   {Type: schema.TypeString, Required: true, Description: "Last address in the range."},
			},
		},
	}
}

func expandAllocationPools(raw []interface{}) []dtgo.AllocationPool {
	// Never nil: the update endpoint validates with Joi.array(), which rejects
	// null outright. dt-go carries no omitempty on this field, so a nil slice
	// would go out as `null` and be refused.
	pools := make([]dtgo.AllocationPool, 0, len(raw))
	for _, item := range raw {
		m := item.(map[string]interface{})
		pools = append(pools, dtgo.AllocationPool{
			Start: m["start"].(string),
			End:   m["end"].(string),
		})
	}
	return pools
}

func flattenAllocationPools(pools []dtgo.AllocationPool) []interface{} {
	out := make([]interface{}, 0, len(pools))
	for _, p := range pools {
		out = append(out, map[string]interface{}{"start": p.Start, "end": p.End})
	}
	return out
}

func expandStringList(raw []interface{}) []string {
	// Same Joi.array() reasoning as expandAllocationPools.
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

// waitForNetwork blocks until a freshly created network can be read back.
//
// The API answers as soon as OpenStack accepts the request and then polls for
// ACTIVE itself over a websocket. Terraform has no websocket, and the details
// endpoint reports no status field to wait on — so the only signal available is
// whether the network resolves at all.
func waitForNetwork(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.Network.GetNetworkDetails(ctx, id)
			if err != nil {
				if dterr.IsNotFound(err) {
					return "waiting", "waiting", nil
				}
				return nil, "", err
			}
			if details.NetworkConfiguration.ID == "" {
				return "waiting", "waiting", nil
			}
			return "done", "done", nil
		},
		Timeout:                   timeout,
		Delay:                     2 * time.Second,
		MinTimeout:                3 * time.Second,
		ContinuousTargetOccurence: 2,
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// waitForNetworkGone blocks until the network stops resolving, so that destroy
// does not return while the platform is still tearing it down.
func waitForNetworkGone(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.Network.GetNetworkDetails(ctx, id)
			if err != nil {
				if dterr.IsNotFound(err) {
					return "done", "done", nil
				}
				return nil, "", err
			}
			if details.NetworkConfiguration.ID == "" {
				return "done", "done", nil
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
