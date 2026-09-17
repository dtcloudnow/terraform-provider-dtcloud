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

// ResourceDtcloudLBMember manages one back-end member of a pool. The id is
// `<lb-id>:<pool-id>:<member-id>`.
//
// One resource per member rather than the batch endpoint, which replaces the
// whole set and would leave two resources owning the same list.
//
// The member list reports only id, name, state and address, so weight, monitor
// overrides and the backup flag are write-only. `protocol_port` is ForceNew:
// import a member only when its port matches the pool's. `name` is write-only
// too — the list reports the VM's name, which is kept under `vm_name`.
func ResourceDtcloudLBMember() *schema.Resource {
	return &schema.Resource{
		Description: "Manages one back-end member of a pool: a machine, the port it serves on, and its share of the traffic.",

		CreateContext: resourceDtcloudLBMemberCreate,
		ReadContext:   resourceDtcloudLBMemberRead,
		UpdateContext: resourceDtcloudLBMemberUpdate,
		DeleteContext: resourceDtcloudLBMemberDelete,
		Importer: &schema.ResourceImporter{
			StateContext: importLBChild("<lb-id>:<pool-id>:<member-id>", "lb_id", "pool_id", "member_id"),
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
				Description:  "ID of the pool this member belongs to.",
			},
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
				Optional:     true,
				ForceNew:     true,
				ValidateFunc: validation.IsPortNumber,
				Description:  "Port the member listens on. Defaults to the pool's port when omitted.",
			},
			"name": {
				Type:     schema.TypeString,
				Optional: true,
				Description: "Name of the member. Write-only: it is stored by the platform " +
					"but never reported back, so it cannot be imported and drift in it is invisible. " +
					"See vm_name for the name the API does report.",
			},
			"weight": {
				Type:        schema.TypeInt,
				Optional:    true,
				Description: "Relative share of traffic this member receives.",
			},
			"enabled": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     true,
				Description: "Whether the member is administratively enabled.",
			},
			"monitor_port": {
				Type:         schema.TypeInt,
				Optional:     true,
				ValidateFunc: validation.IsPortNumber,
				Description:  "Port the health monitor probes, when it differs from protocol_port.",
			},
			"monitor_address": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Address the health monitor probes, when it differs from address.",
			},
			"backup": {
				Type:        schema.TypeBool,
				Optional:    true,
				Description: "Whether this member only receives traffic once the others are down.",
			},

			"member_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "ID of the member on its own, without the load balancer and pool prefixes.",
			},
			"vm_name": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Name of the VM behind this member, as reported by the API.",
			},
			"state": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Operating state reported by the platform.",
			},
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(15 * time.Minute),
			Update: schema.DefaultTimeout(15 * time.Minute),
			Delete: schema.DefaultTimeout(15 * time.Minute),
		},
	}
}

func resourceDtcloudLBMemberCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)
	poolID := bareID(d.Get("pool_id").(string))

	before, _, err := client.LoadBalancer.ListLoadBalancerMembers(ctx, lbID, poolID, nil)
	if err != nil {
		return diag.Errorf("Error listing members of pool %q: %s", poolID, err)
	}
	known := make(map[string]bool, len(before))
	for _, m := range before {
		known[m.ID] = true
	}

	opts := dtgo.CreateLoadBalancerMemberParams{
		Name:            d.Get("name").(string),
		Address:         d.Get("address").(string),
		ComputeServerID: d.Get("compute_server_id").(string),
		AdminStateUp:    boolPtr(d.Get("enabled").(bool)),
	}
	if v, ok := d.GetOk("protocol_port"); ok {
		opts.ProtocolPort = intPtr(v.(int))
	}
	if v, ok := d.GetOk("weight"); ok {
		opts.Weight = intPtr(v.(int))
	}
	if v, ok := d.GetOk("monitor_port"); ok {
		opts.MonitorPort = intPtr(v.(int))
	}
	if v, ok := d.GetOk("monitor_address"); ok {
		opts.MonitorAddress = strPtr(v.(string))
	}
	if v, ok := d.GetOk("backup"); ok {
		opts.Backup = boolPtr(v.(bool))
	}

	if err := mutateChild(ctx, client, lbID, d.Timeout(schema.TimeoutCreate), func() error {
		_, err := client.LoadBalancer.CreateLoadBalancerMember(ctx, lbID, poolID, opts, nil)
		return err
	}); err != nil {
		return diag.Errorf("Error adding member to pool %q: %s", poolID, err)
	}

	var memberID string
	err = waitForCondition(ctx, d.Timeout(schema.TimeoutCreate), func() (bool, error) {
		members, _, err := client.LoadBalancer.ListLoadBalancerMembers(ctx, lbID, poolID, nil)
		if err != nil {
			return false, err
		}
		for _, m := range members {
			if m.ID != "" && !known[m.ID] {
				memberID = m.ID
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return diag.Errorf("Error waiting for the new member in pool %q: %s", poolID, err)
	}

	d.SetId(compositeID(lbID, poolID, memberID))
	d.Set("member_id", memberID)

	return resourceDtcloudLBMemberRead(ctx, d, meta)
}

func resourceDtcloudLBMemberRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)
	poolID := bareID(d.Get("pool_id").(string))
	memberID := d.Get("member_id").(string)

	members, _, err := client.LoadBalancer.ListLoadBalancerMembers(ctx, lbID, poolID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error listing members of pool %q: %s", poolID, err)
	}

	for _, m := range members {
		if m.ID != memberID {
			continue
		}
		// Not d.Set("name", ...): m.Name is the VM's name. See the type comment.
		d.Set("vm_name", m.Name)
		d.Set("state", m.State)
		d.Set("address", m.IPAddress)

		// compute_server_id is ForceNew and the member list does not report it, so an
		// import would land with it empty and the next plan would propose a rebuild.
		// The candidate lookup lists every VM with a port on the network along with
		// its addresses, and the member's address is known. Import only: it costs two
		// extra calls.
		if d.Get("compute_server_id").(string) == "" {
			if id := resolveMemberComputeServerID(ctx, client, lbID, m.IPAddress); id != "" {
				d.Set("compute_server_id", id)
			}
		}
		return nil
	}

	d.SetId("")
	return nil
}

func resourceDtcloudLBMemberUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)
	poolID := bareID(d.Get("pool_id").(string))
	memberID := d.Get("member_id").(string)

	opts := dtgo.UpdateLoadBalancerMemberParams{
		Name:    d.Get("name").(string),
		Enabled: boolPtr(d.Get("enabled").(bool)),
	}
	if v, ok := d.GetOk("weight"); ok {
		opts.Weight = intPtr(v.(int))
	}
	if v, ok := d.GetOk("monitor_port"); ok {
		opts.MonitorPort = intPtr(v.(int))
	}
	if v, ok := d.GetOk("monitor_address"); ok {
		opts.MonitorAddress = v.(string)
	}
	if v, ok := d.GetOk("backup"); ok {
		opts.Backup = boolPtr(v.(bool))
	}

	if err := mutateChild(ctx, client, lbID, d.Timeout(schema.TimeoutUpdate), func() error {
		_, err := client.LoadBalancer.UpdateLoadBalancerMember(ctx, lbID, poolID, memberID, opts, nil)
		return err
	}); err != nil {
		return diag.Errorf("Error updating member %q: %s", memberID, err)
	}
	if err := waitForLBActive(ctx, client, lbID, d.Timeout(schema.TimeoutUpdate)); err != nil {
		return diag.Errorf("Error waiting for load balancer %q after updating member %q: %s", lbID, memberID, err)
	}

	return resourceDtcloudLBMemberRead(ctx, d, meta)
}

func resourceDtcloudLBMemberDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)
	poolID := bareID(d.Get("pool_id").(string))
	memberID := d.Get("member_id").(string)

	if err := mutateChild(ctx, client, lbID, d.Timeout(schema.TimeoutDelete), func() error {
		_, err := client.LoadBalancer.DeleteLoadBalancerMember(ctx, lbID, poolID, memberID, nil)
		if dterr.IsNotFound(err) {
			return nil
		}
		return err
	}); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error removing member %q: %s", memberID, err)
		}
	}

	err := waitForCondition(ctx, d.Timeout(schema.TimeoutDelete), func() (bool, error) {
		members, _, err := client.LoadBalancer.ListLoadBalancerMembers(ctx, lbID, poolID, nil)
		if err != nil {
			if dterr.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		for _, m := range members {
			if m.ID == memberID {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return diag.Errorf("Error waiting for member %q to be removed: %s", memberID, err)
	}

	d.SetId("")
	return nil
}
