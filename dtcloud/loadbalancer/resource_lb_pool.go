package loadbalancer

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

// ResourceDtcloudLBPool manages a back-end pool attached to a listener.
//
// The id is `<lb-id>:<pool-id>`. Only the balancing algorithm and session
// stickiness can be changed in place; the protocol, port and owning listener are
// fixed at creation.
func ResourceDtcloudLBPool() *schema.Resource {
	return &schema.Resource{
		Description: "Manages the back-end pool attached to a listener, and how it spreads connections across its members.",

		CreateContext: resourceDtcloudLBPoolCreate,
		ReadContext:   resourceDtcloudLBPoolRead,
		UpdateContext: resourceDtcloudLBPoolUpdate,
		DeleteContext: resourceDtcloudLBPoolDelete,
		Importer: &schema.ResourceImporter{
			StateContext: importLBChild("<lb-id>:<pool-id>", "lb_id", "pool_id"),
		},

		Schema: map[string]*schema.Schema{
			"lb_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the load balancer this pool belongs to.",
			},
			"listener_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the listener that feeds this pool.",
			},
			"backend_protocol": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringInSlice([]string{"HTTP", "HTTPS", "TCP", "UDP"}, false),
				Description:  "Protocol used to reach the members. One of: HTTP, HTTPS, TCP, UDP.",
			},
			"backend_protocol_port": {
				Type:         schema.TypeInt,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.IsPortNumber,
				Description:  "Port used to reach the members.",
			},
			"lb_algorithm": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.StringInSlice([]string{"LEAST_CONNECTIONS", "ROUND_ROBIN", "SOURCE_IP"}, false),
				Description:  "Balancing algorithm. One of: LEAST_CONNECTIONS, ROUND_ROBIN, SOURCE_IP. Can be changed in place.",
			},
			"sticky_session": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     false,
				Description: "Whether to pin a client to the member it first reached. Can be changed in place.",
			},

			"pool_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "ID of the pool on its own, without the load balancer prefix.",
			},
			"name":                {Type: schema.TypeString, Computed: true, Description: "Name assigned by the platform."},
			"provisioning_status": {Type: schema.TypeString, Computed: true, Description: "Provisioning status."},
			"operating_status":    {Type: schema.TypeString, Computed: true, Description: "Operating status."},
			"health_monitor_id":   {Type: schema.TypeString, Computed: true, Description: "ID of the health monitor attached to this pool, if any."},
			"member_ids": {
				Type:        schema.TypeList,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "IDs of the members currently in the pool.",
			},
			"created_at": {Type: schema.TypeString, Computed: true, Description: "Creation timestamp as reported by the API."},
			"updated_at": {Type: schema.TypeString, Computed: true, Description: "Last-update timestamp as reported by the API."},
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(15 * time.Minute),
			Update: schema.DefaultTimeout(15 * time.Minute),
			Delete: schema.DefaultTimeout(15 * time.Minute),
		},
	}
}

func resourceDtcloudLBPoolCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)

	before, _, err := client.LoadBalancer.ListLoadBalancerPools(ctx, lbID, nil)
	if err != nil {
		return diag.Errorf("Error listing pools on load balancer %q: %s", lbID, err)
	}
	known := make(map[string]bool, len(before))
	for _, p := range before {
		known[p.ID] = true
	}

	opts := dtgo.CreateLoadBalancerPoolParams{
		ListenerID:          bareID(d.Get("listener_id").(string)),
		BackendProtocol:     d.Get("backend_protocol").(string),
		BackendProtocolPort: d.Get("backend_protocol_port").(int),
		LBAlgorithm:         d.Get("lb_algorithm").(string),
	}
	if err := mutateChild(ctx, client, lbID, d.Timeout(schema.TimeoutCreate), func() error {
		_, err := client.LoadBalancer.CreateLoadBalancerPool(ctx, lbID, opts, nil)
		return err
	}); err != nil {
		return diag.Errorf("Error creating pool on load balancer %q: %s", lbID, err)
	}

	var poolID string
	err = waitForCondition(ctx, d.Timeout(schema.TimeoutCreate), func() (bool, error) {
		pools, _, err := client.LoadBalancer.ListLoadBalancerPools(ctx, lbID, nil)
		if err != nil {
			return false, err
		}
		for _, p := range pools {
			if p.ID != "" && !known[p.ID] {
				poolID = p.ID
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return diag.Errorf("Error waiting for the new pool on load balancer %q: %s", lbID, err)
	}

	d.SetId(compositeID(lbID, poolID))
	d.Set("pool_id", poolID)

	// sticky_session is not part of create.
	if d.Get("sticky_session").(bool) {
		if diags := applyPoolUpdate(ctx, d, meta); diags.HasError() {
			return diags
		}
	}

	return resourceDtcloudLBPoolRead(ctx, d, meta)
}

func resourceDtcloudLBPoolRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)
	poolID := d.Get("pool_id").(string)

	pools, _, err := client.LoadBalancer.ListLoadBalancerPools(ctx, lbID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error listing pools on load balancer %q: %s", lbID, err)
	}

	for _, p := range pools {
		if p.ID != poolID {
			continue
		}
		d.Set("name", p.Name)
		d.Set("backend_protocol", p.Protocol)
		d.Set("backend_protocol_port", p.DefaultProtocolPort)
		d.Set("lb_algorithm", p.LbAlgorithm)
		d.Set("provisioning_status", p.ProvisioningStatus)
		d.Set("operating_status", p.OperatingStatus)
		d.Set("health_monitor_id", p.HealthMonitorID)
		d.Set("created_at", p.CreatedAt)
		d.Set("updated_at", p.UpdatedAt)

		members := make([]string, 0, len(p.Members))
		for _, m := range p.Members {
			members = append(members, m.ID)
		}
		d.Set("member_ids", members)

		if len(p.Listeners) > 0 {
			d.Set("listener_id", p.Listeners[0].ID)
		}
		return nil
	}

	d.SetId("")
	return nil
}

func resourceDtcloudLBPoolUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	if diags := applyPoolUpdate(ctx, d, meta); diags.HasError() {
		return diags
	}
	return resourceDtcloudLBPoolRead(ctx, d, meta)
}

func applyPoolUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)
	poolID := d.Get("pool_id").(string)

	opts := dtgo.UpdateLoadBalancerPoolParams{
		LBAlgorithm:   d.Get("lb_algorithm").(string),
		StickySession: boolPtr(d.Get("sticky_session").(bool)),
	}
	if err := mutateChild(ctx, client, lbID, d.Timeout(schema.TimeoutUpdate), func() error {
		_, err := client.LoadBalancer.UpdateLoadBalancerPool(ctx, lbID, poolID, opts, nil)
		return err
	}); err != nil {
		return diag.Errorf("Error updating pool %q: %s", poolID, err)
	}
	if err := waitForLBActive(ctx, client, lbID, d.Timeout(schema.TimeoutUpdate)); err != nil {
		return diag.Errorf("Error waiting for load balancer %q after updating pool %q: %s", lbID, poolID, err)
	}
	return nil
}

func resourceDtcloudLBPoolDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)
	poolID := d.Get("pool_id").(string)

	if err := mutateChild(ctx, client, lbID, d.Timeout(schema.TimeoutDelete), func() error {
		_, err := client.LoadBalancer.DeleteLoadBalancerPool(ctx, lbID, poolID, nil)
		if dterr.IsNotFound(err) {
			return nil
		}
		return err
	}); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error deleting pool %q: %s", poolID, err)
		}
	}

	err := waitForCondition(ctx, d.Timeout(schema.TimeoutDelete), func() (bool, error) {
		pools, _, err := client.LoadBalancer.ListLoadBalancerPools(ctx, lbID, nil)
		if err != nil {
			if dterr.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		for _, p := range pools {
			if p.ID == poolID {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return diag.Errorf("Error waiting for pool %q to be deleted: %s", poolID, err)
	}

	d.SetId("")
	return nil
}
