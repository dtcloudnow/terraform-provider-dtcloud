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

// ResourceDtcloudLBHealthMonitor manages the health check attached to a pool.
// The id is `<lb-id>:<health-monitor-id>`, and a pool has at most one monitor.
//
// `type` and `pool_id` are fixed at creation — update accepts only the timings,
// thresholds and URL path.
func ResourceDtcloudLBHealthMonitor() *schema.Resource {
	return &schema.Resource{
		Description: "Manages the health check attached to a pool, which decides whether a member still receives traffic.",

		CreateContext: resourceDtcloudLBHealthMonitorCreate,
		ReadContext:   resourceDtcloudLBHealthMonitorRead,
		UpdateContext: resourceDtcloudLBHealthMonitorUpdate,
		DeleteContext: resourceDtcloudLBHealthMonitorDelete,
		Importer: &schema.ResourceImporter{
			StateContext: importLBChild("<lb-id>:<health-monitor-id>", "lb_id", "health_monitor_id"),
		},

		Schema: map[string]*schema.Schema{
			"lb_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the load balancer.",
			},
			"pool_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the pool to monitor. A pool can have only one monitor.",
			},
			"type": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringInSlice([]string{"HTTP", "HTTPS", "TCP", "PING"}, false),
				Description:  "Probe type. One of: HTTP, HTTPS, TCP, PING.",
			},
			"interval": {
				Type:         schema.TypeInt,
				Required:     true,
				ValidateFunc: validation.IntBetween(5, 300),
				Description:  "Seconds between probes (5-300).",
			},
			"timeout": {
				Type:         schema.TypeInt,
				Required:     true,
				ValidateFunc: validation.IntBetween(5, 60),
				Description:  "Seconds to wait for a probe response (5-60).",
			},
			"healthy_threshold": {
				Type:         schema.TypeInt,
				Required:     true,
				ValidateFunc: validation.IntBetween(1, 10),
				Description:  "Consecutive successes before a member counts as healthy (1-10).",
			},
			"unhealthy_threshold": {
				Type:         schema.TypeInt,
				Required:     true,
				ValidateFunc: validation.IntBetween(1, 10),
				Description:  "Consecutive failures before a member counts as unhealthy (1-10).",
			},
			"url_path": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Path requested by HTTP and HTTPS probes, e.g. /healthz.",
			},

			"health_monitor_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "ID of the monitor on its own, without the load balancer prefix.",
			},
			"name":                {Type: schema.TypeString, Computed: true, Description: "Name assigned by the platform."},
			"http_method":         {Type: schema.TypeString, Computed: true, Description: "HTTP method used by the probe."},
			"expected_codes":      {Type: schema.TypeString, Computed: true, Description: "Response codes treated as healthy."},
			"provisioning_status": {Type: schema.TypeString, Computed: true, Description: "Provisioning status."},
			"operating_status":    {Type: schema.TypeString, Computed: true, Description: "Operating status."},
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(15 * time.Minute),
			Update: schema.DefaultTimeout(15 * time.Minute),
			Delete: schema.DefaultTimeout(15 * time.Minute),
		},
	}
}

func resourceDtcloudLBHealthMonitorCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)

	before, _, err := client.LoadBalancer.ListLoadBalancerHealthMonitors(ctx, lbID, nil)
	if err != nil {
		return diag.Errorf("Error listing health monitors on load balancer %q: %s", lbID, err)
	}
	known := make(map[string]bool, len(before.HealthMonitors))
	for _, h := range before.HealthMonitors {
		known[h.ID] = true
	}

	opts := dtgo.CreateLoadBalancerHealthMonitorParams{
		PoolID:             bareID(d.Get("pool_id").(string)),
		Interval:           d.Get("interval").(int),
		Timeout:            d.Get("timeout").(int),
		HealthyThreshold:   d.Get("healthy_threshold").(int),
		UnhealthyThreshold: d.Get("unhealthy_threshold").(int),
		Type:               d.Get("type").(string),
	}
	if v, ok := d.GetOk("url_path"); ok {
		opts.URLPath = strPtr(v.(string))
	}

	if err := mutateChild(ctx, client, lbID, d.Timeout(schema.TimeoutCreate), func() error {
		_, err := client.LoadBalancer.CreateLoadBalancerHealthMonitor(ctx, lbID, opts, nil)
		return err
	}); err != nil {
		return diag.Errorf("Error creating health monitor on load balancer %q: %s", lbID, err)
	}

	var hmID string
	err = waitForCondition(ctx, d.Timeout(schema.TimeoutCreate), func() (bool, error) {
		monitors, _, err := client.LoadBalancer.ListLoadBalancerHealthMonitors(ctx, lbID, nil)
		if err != nil {
			return false, err
		}
		for _, h := range monitors.HealthMonitors {
			if h.ID != "" && !known[h.ID] {
				hmID = h.ID
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return diag.Errorf("Error waiting for the new health monitor on load balancer %q: %s", lbID, err)
	}

	d.SetId(compositeID(lbID, hmID))
	d.Set("health_monitor_id", hmID)

	return resourceDtcloudLBHealthMonitorRead(ctx, d, meta)
}

func resourceDtcloudLBHealthMonitorRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)
	hmID := d.Get("health_monitor_id").(string)

	monitors, _, err := client.LoadBalancer.ListLoadBalancerHealthMonitors(ctx, lbID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error listing health monitors on load balancer %q: %s", lbID, err)
	}

	for _, h := range monitors.HealthMonitors {
		if h.ID != hmID {
			continue
		}
		d.Set("name", h.Name)
		d.Set("type", h.Type)
		// The wire names differ: delay is the probe interval, and the retry
		// counters are the thresholds.
		d.Set("interval", h.Delay)
		d.Set("timeout", h.Timeout)
		d.Set("healthy_threshold", h.MaxRetries)
		d.Set("unhealthy_threshold", h.MaxRetriesDown)
		d.Set("url_path", h.URLPath)
		d.Set("http_method", h.HTTPMethod)
		d.Set("expected_codes", h.ExpectedCodes)
		d.Set("provisioning_status", h.ProvisioningStatus)
		d.Set("operating_status", h.OperatingStatus)
		if len(h.Pools) > 0 {
			d.Set("pool_id", h.Pools[0].ID)
		}
		return nil
	}

	d.SetId("")
	return nil
}

func resourceDtcloudLBHealthMonitorUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)
	hmID := d.Get("health_monitor_id").(string)

	opts := dtgo.UpdateLoadBalancerHealthMonitorParams{
		Interval:           intPtr(d.Get("interval").(int)),
		Timeout:            intPtr(d.Get("timeout").(int)),
		HealthyThreshold:   intPtr(d.Get("healthy_threshold").(int)),
		UnhealthyThreshold: intPtr(d.Get("unhealthy_threshold").(int)),
	}
	if v, ok := d.GetOk("url_path"); ok {
		opts.URLPath = strPtr(v.(string))
	}

	if err := mutateChild(ctx, client, lbID, d.Timeout(schema.TimeoutUpdate), func() error {
		_, err := client.LoadBalancer.UpdateLoadBalancerHealthMonitor(ctx, lbID, hmID, opts, nil)
		return err
	}); err != nil {
		return diag.Errorf("Error updating health monitor %q: %s", hmID, err)
	}
	if err := waitForLBActive(ctx, client, lbID, d.Timeout(schema.TimeoutUpdate)); err != nil {
		return diag.Errorf("Error waiting for load balancer %q after updating health monitor %q: %s", lbID, hmID, err)
	}

	return resourceDtcloudLBHealthMonitorRead(ctx, d, meta)
}

func resourceDtcloudLBHealthMonitorDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)
	hmID := d.Get("health_monitor_id").(string)

	if err := mutateChild(ctx, client, lbID, d.Timeout(schema.TimeoutDelete), func() error {
		_, err := client.LoadBalancer.DeleteLoadBalancerHealthMonitor(ctx, lbID, hmID, nil)
		if dterr.IsNotFound(err) {
			return nil
		}
		return err
	}); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error deleting health monitor %q: %s", hmID, err)
		}
	}

	err := waitForCondition(ctx, d.Timeout(schema.TimeoutDelete), func() (bool, error) {
		monitors, _, err := client.LoadBalancer.ListLoadBalancerHealthMonitors(ctx, lbID, nil)
		if err != nil {
			if dterr.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		for _, h := range monitors.HealthMonitors {
			if h.ID == hmID {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return diag.Errorf("Error waiting for health monitor %q to be deleted: %s", hmID, err)
	}

	d.SetId("")
	return nil
}
