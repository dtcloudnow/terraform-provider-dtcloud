package loadbalancer

import (
	"context"
	"encoding/json"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudLB manages a load balancer.
//
// The nested create form, which builds listeners and pools in one call, is not
// exposed: Terraform would own objects it has no ids for and cannot address
// individually. Use the granular resources alongside this one, or
// dtcloud_lb_balancing_pool for the one-shot shape.
//
// `name`, `description` and `enabled` are the only updatable arguments, and
// `enabled` is not reported back, so an import shows one in-place update.
func ResourceDtcloudLB() *schema.Resource {
	return &schema.Resource{
		Description: "Manages a load balancer -- the front door that accepts connections and the object every listener, pool and member hangs off.",

		CreateContext: resourceDtcloudLBCreate,
		ReadContext:   resourceDtcloudLBRead,
		UpdateContext: resourceDtcloudLBUpdate,
		DeleteContext: resourceDtcloudLBDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "Name of the load balancer. Can be changed in place.",
			},
			"flavor_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the load balancer flavor (sizing).",
			},
			"network_type": {
				Type:         schema.TypeString,
				Optional:     true,
				ForceNew:     true,
				Default:      "Virtual",
				ValidateFunc: validation.StringInSlice([]string{"Virtual"}, false),
				Description:  "Network type. The API currently accepts only `Virtual`.",
			},
			"description": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Free-text description. Can be changed in place.",
			},
			"enabled": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     true,
				Description: "Whether the load balancer is administratively enabled.",
			},

			// Exactly one of these three decides where the VIP lives. Declaring the
			// rule here turns a server-side rejection into a plan-time error.
			"vip_network_id": {
				Type:         schema.TypeString,
				Optional:     true,
				ForceNew:     true,
				ExactlyOneOf: []string{"vip_network_id", "vip_subnet_id", "vip_port_id"},
				Description:  "Network to place the VIP on.",
			},
			"vip_subnet_id": {
				Type:         schema.TypeString,
				Optional:     true,
				ForceNew:     true,
				ExactlyOneOf: []string{"vip_network_id", "vip_subnet_id", "vip_port_id"},
				Description:  "Subnet to place the VIP on.",
			},
			"vip_port_id": {
				Type:         schema.TypeString,
				Optional:     true,
				ForceNew:     true,
				ExactlyOneOf: []string{"vip_network_id", "vip_subnet_id", "vip_port_id"},
				Description:  "Existing port to use for the VIP.",
			},
			"vip_address": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				ForceNew:    true,
				Description: "Specific VIP address to request. Allocated by the platform when omitted.",
			},
			"floating_ip": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				ForceNew:    true,
				Description: "Floating IP to associate with the load balancer.",
			},
			"external_network_id": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "External network used when a floating IP is requested.",
			},

			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Provisioning status, e.g. ACTIVE or ERROR.",
			},
			"flavor_name": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Human-readable name of the flavor.",
			},
			"high_availability": {
				Type:     schema.TypeString,
				Computed: true,
				Description: "High-availability mode, reported as Enabled or Disabled. " +
					"It is not settable here: HA is chosen by picking a flavor whose `ha` " +
					"flag is true, so this reports back the flavor's setting.",
			},
			"balancing_pools": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Number of balancing pools configured on the load balancer.",
			},
			"members_total": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Total number of members across all pools.",
			},
			"members_state": {
				Type:        schema.TypeList,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "Operating state of each member.",
			},
			"created_on": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "When the load balancer was created, as reported by the API.",
			},
			"network": lbNetworkSchema(),
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(30 * time.Minute),
			Update: schema.DefaultTimeout(15 * time.Minute),
			Delete: schema.DefaultTimeout(20 * time.Minute),
		},
	}
}

func resourceDtcloudLBCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	opts := dtgo.CreateLoadBalancerParams{
		Name:              d.Get("name").(string),
		Description:       d.Get("description").(string),
		FlavorID:          d.Get("flavor_id").(string),
		NetworkType:       d.Get("network_type").(string),
		VipAddress:        d.Get("vip_address").(string),
		VipNetworkID:      d.Get("vip_network_id").(string),
		VipSubnetID:       d.Get("vip_subnet_id").(string),
		VipPortID:         d.Get("vip_port_id").(string),
		FloatingIP:        d.Get("floating_ip").(string),
		ExternalNetworkID: d.Get("external_network_id").(string),
	}

	body, err := client.LoadBalancer.CreateLoadBalancer(ctx, opts, nil)
	if err != nil {
		return diag.Errorf("Error creating load balancer: %s", err)
	}

	var created createLBResponse
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.LoadBalancer.ID == "" {
		return diag.Errorf("Error reading the created load balancer's id from the API response (body: %s)", body)
	}
	d.SetId(created.LoadBalancer.ID)

	if err := waitForLBActive(ctx, client, d.Id(), d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.Errorf("Error waiting for load balancer %q to become ACTIVE: %s", d.Id(), err)
	}

	// `enabled` is not part of create; apply it if the user asked for false.
	if !d.Get("enabled").(bool) {
		if _, err := client.LoadBalancer.UpdateLoadBalancer(ctx, d.Id(), dtgo.UpdateLoadBalancerParams{
			Enabled: boolPtr(false),
		}, nil); err != nil {
			return diag.Errorf("Error disabling load balancer %q: %s", d.Id(), err)
		}
	}

	return resourceDtcloudLBRead(ctx, d, meta)
}

func resourceDtcloudLBRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	details, _, err := client.LoadBalancer.GetLoadBalancerDetails(ctx, d.Id(), nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error retrieving load balancer %q: %s", d.Id(), err)
	}
	if details.ID == "" {
		d.SetId("")
		return nil
	}

	d.Set("name", details.Name)
	d.Set("description", details.Description)
	d.Set("status", details.Status)
	d.Set("floating_ip", details.FloatingIP)
	d.Set("flavor_name", details.FlavorName)
	d.Set("high_availability", details.HighAvailability)
	d.Set("balancing_pools", details.BalancingPools)
	d.Set("members_total", details.MembersTotal)
	d.Set("members_state", details.MembersState)
	d.Set("created_on", details.CreatedOn)
	d.Set("network", flattenLBNetwork(details.Network))

	// Only the flavor name and HA setting are reported; resolve them so that
	// flavor_id survives an import.
	if id := resolveFlavorID(ctx, client, details.FlavorName, details.HighAvailability == "Enabled"); id != "" {
		d.Set("flavor_id", id)
	}
	// The VIP is reported through the network block, not a top-level field.
	if details.Network.IP != "" {
		d.Set("vip_address", details.Network.IP)
	}

	// Two fields nothing echoes back, filled in for `terraform import` only. Both
	// are ForceNew, so leaving them empty makes the next plan propose a rebuild.
	// The VIP anchor is guessed only when state holds none of the three: what is
	// reported is where the VIP ended up, not which anchor was asked for.
	if d.Get("vip_network_id").(string) == "" && d.Get("vip_subnet_id").(string) == "" && d.Get("vip_port_id").(string) == "" {
		d.Set("vip_network_id", details.Network.ID)
	}
	// network_type is write-only and takes one value, so there is nothing to guess.
	if d.Get("network_type").(string) == "" {
		d.Set("network_type", "Virtual")
	}

	return nil
}

func resourceDtcloudLBUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	if !d.HasChanges("name", "description", "enabled") {
		return resourceDtcloudLBRead(ctx, d, meta)
	}

	opts := dtgo.UpdateLoadBalancerParams{
		Name:        d.Get("name").(string),
		Description: d.Get("description").(string),
		Enabled:     boolPtr(d.Get("enabled").(bool)),
	}
	if err := mutateChild(ctx, client, d.Id(), d.Timeout(schema.TimeoutUpdate), func() error {
		_, err := client.LoadBalancer.UpdateLoadBalancer(ctx, d.Id(), opts, nil)
		return err
	}); err != nil {
		return diag.Errorf("Error updating load balancer %q: %s", d.Id(), err)
	}

	if err := waitForLBActive(ctx, client, d.Id(), d.Timeout(schema.TimeoutUpdate)); err != nil {
		return diag.Errorf("Error waiting for load balancer %q after update: %s", d.Id(), err)
	}

	return resourceDtcloudLBRead(ctx, d, meta)
}

func resourceDtcloudLBDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	if err := mutateChild(ctx, client, d.Id(), d.Timeout(schema.TimeoutDelete), func() error {
		_, err := client.LoadBalancer.DeleteLoadBalancer(ctx, d.Id(), nil)
		if dterr.IsNotFound(err) {
			return nil
		}
		return err
	}); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error deleting load balancer %q: %s", d.Id(), err)
		}
	}

	if err := waitForLBGone(ctx, client, d.Id(), d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.Errorf("Error waiting for load balancer %q to be deleted: %s", d.Id(), err)
	}

	d.SetId("")
	return nil
}
