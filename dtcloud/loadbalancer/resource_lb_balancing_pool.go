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

// ResourceDtcloudLBBalancingPool builds a listener, its pool, a health monitor
// and the members in a single call.
//
// There is no update endpoint for the chain as a unit, so every argument here is
// ForceNew — changing the probe interval recreates the listener and drops the
// members with it. `member` blocks do not survive `terraform import` either:
// nothing reports a member's port.
//
// Prefer the granular resources unless the atomic shape is what you want, and do
// not mix the two styles on the same load balancer.
func ResourceDtcloudLBBalancingPool() *schema.Resource {
	return &schema.Resource{
		Description: "Builds a listener, its pool, a health monitor and the members in a single call. A create-only shortcut; prefer the granular resources.",

		CreateContext: resourceDtcloudLBBalancingPoolCreate,
		ReadContext:   resourceDtcloudLBBalancingPoolRead,
		DeleteContext: resourceDtcloudLBBalancingPoolDelete,
		Importer: &schema.ResourceImporter{
			StateContext: importLBChild("<lb-id>:<balancing-pool-id>", "lb_id", "balancing_pool_id"),
		},

		Schema: map[string]*schema.Schema{
			"lb_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the load balancer.",
			},

			// Front end
			"lb_protocol": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringInSlice([]string{"HTTP", "HTTPS", "TCP", "UDP", "TERMINATED_HTTPS"}, false),
				Description:  "Front-end protocol. One of: HTTP, HTTPS, TCP, UDP, TERMINATED_HTTPS.",
			},
			"lb_port": {
				Type:         schema.TypeInt,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.IsPortNumber,
				Description:  "Front-end port.",
			},
			"lb_algorithm": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringInSlice([]string{"LEAST_CONNECTIONS", "ROUND_ROBIN", "SOURCE_IP"}, false),
				Description:  "Balancing algorithm.",
			},
			"sticky_session": {
				Type:        schema.TypeBool,
				Required:    true,
				ForceNew:    true,
				Description: "Whether to pin a client to the member it first reached.",
			},

			// Back end
			"backend_protocol": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringInSlice([]string{"HTTP", "HTTPS", "TCP", "UDP"}, false),
				Description:  "Protocol used to reach the members.",
			},
			"backend_protocol_port": {
				Type:         schema.TypeInt,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.IsPortNumber,
				Description:  "Port used to reach the members.",
			},

			// Health monitor
			"type": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringInSlice([]string{"HTTP", "HTTPS", "TCP", "PING", "UDP-CONNECT"}, false),
				Description:  "Health probe type.",
			},
			"interval": {
				Type:         schema.TypeInt,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.IntBetween(5, 300),
				Description:  "Seconds between probes (5-300).",
			},
			"timeout": {
				Type:         schema.TypeInt,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.IntBetween(5, 60),
				Description:  "Seconds to wait for a probe response (5-60).",
			},
			"healthy_threshold": {
				Type:         schema.TypeInt,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.IntBetween(1, 10),
				Description:  "Consecutive successes before a member counts as healthy.",
			},
			"unhealthy_threshold": {
				Type:         schema.TypeInt,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.IntBetween(1, 10),
				Description:  "Consecutive failures before a member counts as unhealthy.",
			},
			"url_path": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				ForceNew:    true,
				Description: "Path requested by HTTP and HTTPS probes.",
			},

			// TLS
			"certificate": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Sensitive:   true,
				Description: "PEM certificate for TERMINATED_HTTPS.",
			},
			"private_key": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Sensitive:   true,
				Description: "PEM private key for TERMINATED_HTTPS.",
			},

			// Listener tuning
			"connection_limit": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				ForceNew:    true,
				Description: "Maximum number of concurrent connections. -1 means no limit.",
			},
			"allowed_cidrs": {
				Type:        schema.TypeList,
				Optional:    true,
				ForceNew:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "CIDRs permitted to reach the listener.",
			},
			// Seconds here, milliseconds on dtcloud_lb_listener: this endpoint converts
			// and the listener endpoint does not. Computed as well as Optional because
			// the platform fills these in when they are left out and reports what it
			// filled in — Optional alone would propose a rebuild on every plan.
			"timeout_client_data":    {Type: schema.TypeInt, Optional: true, Computed: true, ForceNew: true, Description: "Client inactivity timeout in SECONDS (note: dtcloud_lb_listener takes milliseconds)."},
			"timeout_member_connect": {Type: schema.TypeInt, Optional: true, Computed: true, ForceNew: true, Description: "Member connect timeout in SECONDS (note: dtcloud_lb_listener takes milliseconds)."},
			"timeout_member_data":    {Type: schema.TypeInt, Optional: true, Computed: true, ForceNew: true, Description: "Member inactivity timeout in SECONDS (note: dtcloud_lb_listener takes milliseconds)."},

			"member": {
				Type:        schema.TypeList,
				Optional:    true,
				ForceNew:    true,
				Description: "Back-end members created together with the pool.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"address": {
							Type:         schema.TypeString,
							Required:     true,
							ForceNew:     true,
							ValidateFunc: validation.IsIPAddress,
							Description:  "IP address of the member.",
						},
						"compute_server_id": {
							Type:         schema.TypeString,
							Required:     true,
							ForceNew:     true,
							ValidateFunc: validation.NoZeroValues,
							Description:  "ID of the VM behind this address.",
						},
						"protocol_port": {
							Type:         schema.TypeInt,
							Required:     true,
							ForceNew:     true,
							ValidateFunc: validation.IsPortNumber,
							Description:  "Port the member listens on.",
						},
					},
				},
			},

			"balancing_pool_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "ID of the balancing pool on its own, without the load balancer prefix.",
			},
			"listener_id":       {Type: schema.TypeString, Computed: true, Description: "ID of the listener the platform created."},
			"health_monitor_id": {Type: schema.TypeString, Computed: true, Description: "ID of the health monitor the platform created."},
			"status":            {Type: schema.TypeString, Computed: true, Description: "Status reported by the platform."},
			"members_total":     {Type: schema.TypeInt, Computed: true, Description: "Number of members in the pool."},
			"members_state": {
				Type:        schema.TypeList,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "Operating state of each member.",
			},
			"insert_headers": insertHeadersSchema(true),
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(20 * time.Minute),
			Delete: schema.DefaultTimeout(20 * time.Minute),
		},
	}
}

func resourceDtcloudLBBalancingPoolCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)

	before, _, err := client.LoadBalancer.ListLoadBalancerBalancingPools(ctx, lbID, nil)
	if err != nil {
		return diag.Errorf("Error listing balancing pools on load balancer %q: %s", lbID, err)
	}
	known := make(map[string]bool, len(before))
	for _, p := range before {
		known[p.ID] = true
	}

	members := make([]dtgo.Member, 0)
	for _, raw := range d.Get("member").([]interface{}) {
		m := raw.(map[string]interface{})
		members = append(members, dtgo.Member{
			Address:         m["address"].(string),
			ComputeServerID: m["compute_server_id"].(string),
			ProtocolPort:    m["protocol_port"].(int),
		})
	}

	opts := dtgo.CreateLoadBalancerBalancingPoolParams{
		StickySession:      d.Get("sticky_session").(bool),
		Certificate:        d.Get("certificate").(string),
		PrivateKey:         d.Get("private_key").(string),
		Interval:           d.Get("interval").(int),
		Timeout:            d.Get("timeout").(int),
		HealthyThreshold:   d.Get("healthy_threshold").(int),
		UnhealthyThreshold: d.Get("unhealthy_threshold").(int),
		Type:               d.Get("type").(string),
		URLPath:            d.Get("url_path").(string),
		BackendProtocol:    d.Get("backend_protocol").(string),
		BackendPort:        d.Get("backend_protocol_port").(int),
		LBAlgorithm:        d.Get("lb_algorithm").(string),
		LBProtocol:         d.Get("lb_protocol").(string),
		LBPort:             d.Get("lb_port").(int),
		AllowedCIDRs:       expandStringList(d.Get("allowed_cidrs").([]interface{})),
		InsertHeaders:      expandInsertHeaders(d.Get("insert_headers").([]interface{})),
		Members:            members,
	}
	if v, ok := d.GetOk("connection_limit"); ok {
		opts.ConnectionLimit = intPtr(v.(int))
	}
	if v, ok := d.GetOk("timeout_client_data"); ok {
		opts.TimeoutClientData = intPtr(v.(int))
	}
	if v, ok := d.GetOk("timeout_member_connect"); ok {
		opts.TimeoutMemberConnect = intPtr(v.(int))
	}
	if v, ok := d.GetOk("timeout_member_data"); ok {
		opts.TimeoutMemberData = intPtr(v.(int))
	}

	if err := mutateChild(ctx, client, lbID, d.Timeout(schema.TimeoutCreate), func() error {
		_, err := client.LoadBalancer.CreateLoadBalancerBalancingPool(ctx, lbID, opts, nil)
		return err
	}); err != nil {
		return diag.Errorf("Error creating balancing pool on load balancer %q: %s", lbID, err)
	}

	var bpID string
	err = waitForCondition(ctx, d.Timeout(schema.TimeoutCreate), func() (bool, error) {
		pools, _, err := client.LoadBalancer.ListLoadBalancerBalancingPools(ctx, lbID, nil)
		if err != nil {
			return false, err
		}
		for _, p := range pools {
			if p.ID != "" && !known[p.ID] {
				bpID = p.ID
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return diag.Errorf("Error waiting for the new balancing pool on load balancer %q: %s", lbID, err)
	}

	d.SetId(compositeID(lbID, bpID))
	d.Set("balancing_pool_id", bpID)

	if err := waitForLBActive(ctx, client, lbID, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.Errorf("Error waiting for load balancer %q after creating the balancing pool: %s", lbID, err)
	}

	return resourceDtcloudLBBalancingPoolRead(ctx, d, meta)
}

// balancingAlgorithms maps the display names the details endpoint reports back
// to the values create accepts: "Round robin" where it was given "ROUND_ROBIN".
var balancingAlgorithms = map[string]string{
	"Round robin":       "ROUND_ROBIN",
	"Source IP":         "SOURCE_IP",
	"Least connections": "LEAST_CONNECTIONS",
}

func resourceDtcloudLBBalancingPoolRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)
	bpID := d.Get("balancing_pool_id").(string)

	// The details endpoint, not the list one: every argument here is ForceNew, so a
	// field the read cannot recover makes an import propose to tear the pool down.
	details, _, err := client.LoadBalancer.GetLoadBalancerBalancingPoolDetails(ctx, lbID, bpID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error retrieving balancing pool %q on load balancer %q: %s", bpID, lbID, err)
	}
	if details.ID == "" {
		d.SetId("")
		return nil
	}

	d.Set("listener_id", details.ListenerID)
	d.Set("health_monitor_id", details.HealthMonitorID)
	d.Set("status", details.Status)
	d.Set("members_total", details.MembersTotal)
	d.Set("members_state", details.MembersState)

	d.Set("lb_protocol", details.LbProtocol)
	d.Set("lb_port", details.LbProtocolPort)
	d.Set("backend_protocol", details.BackEndProtocol)
	d.Set("backend_protocol_port", details.BackEndProtocolPort)
	d.Set("sticky_session", details.StickySession == "Enabled")
	if algorithm, ok := balancingAlgorithms[details.BalancingAlgorithm]; ok {
		d.Set("lb_algorithm", algorithm)
	}

	// Health monitor. `protocol` is the probe type, not a network protocol.
	d.Set("type", details.Protocol)
	d.Set("url_path", details.URLPath)
	d.Set("interval", details.Interval)
	d.Set("timeout", details.Timeout)
	d.Set("healthy_threshold", details.HealthyThreshold)
	d.Set("unhealthy_threshold", details.UnHealthyThreshold)

	// Listener tuning. Reported whether or not they were asked for, which is safe
	// only because the schema marks them Computed.
	d.Set("connection_limit", details.ConnectionLimit)
	// `null` when no CIDRs were given, which would become an empty list in state
	// and, since allowed_cidrs is ForceNew, read as "rebuild the pool".
	if len(details.AllowedCidrs) > 0 {
		d.Set("allowed_cidrs", details.AllowedCidrs)
	}
	d.Set("insert_headers", flattenInsertHeaders(details.InsertHeaders))
	d.Set("timeout_client_data", details.TimeoutClientData)
	d.Set("timeout_member_connect", details.TimeoutMemberConnect)
	d.Set("timeout_member_data", details.TimeoutMemberData)

	return nil
}

func resourceDtcloudLBBalancingPoolDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)
	bpID := d.Get("balancing_pool_id").(string)

	if err := mutateChild(ctx, client, lbID, d.Timeout(schema.TimeoutDelete), func() error {
		_, err := client.LoadBalancer.DeleteLoadBalancerBalancingPool(ctx, lbID, bpID, nil)
		if dterr.IsNotFound(err) {
			return nil
		}
		return err
	}); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error deleting balancing pool %q: %s", bpID, err)
		}
	}

	err := waitForCondition(ctx, d.Timeout(schema.TimeoutDelete), func() (bool, error) {
		pools, _, err := client.LoadBalancer.ListLoadBalancerBalancingPools(ctx, lbID, nil)
		if err != nil {
			if dterr.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		for _, p := range pools {
			if p.ID == bpID {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return diag.Errorf("Error waiting for balancing pool %q to be deleted: %s", bpID, err)
	}

	d.SetId("")
	return nil
}
