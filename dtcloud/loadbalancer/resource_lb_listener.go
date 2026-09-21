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

// ResourceDtcloudLBListener manages a listener on a load balancer. The id is
// `<lb-id>:<listener-id>`, because every listener call is scoped to its load
// balancer and the listener id alone cannot address it.
//
// Read goes through the list endpoint rather than the details one: the list is
// typed where details is not, and finding the entry costs one call either way.
func ResourceDtcloudLBListener() *schema.Resource {
	return &schema.Resource{
		Description: "Manages a listener on a load balancer: the protocol and port it accepts connections on.",

		CreateContext: resourceDtcloudLBListenerCreate,
		ReadContext:   resourceDtcloudLBListenerRead,
		UpdateContext: resourceDtcloudLBListenerUpdate,
		DeleteContext: resourceDtcloudLBListenerDelete,
		Importer: &schema.ResourceImporter{
			StateContext: importLBChild("<lb-id>:<listener-id>", "lb_id", "listener_id"),
		},

		Schema: map[string]*schema.Schema{
			"lb_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the load balancer this listener belongs to.",
			},
			"protocol": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringInSlice([]string{"HTTP", "HTTPS", "TCP", "UDP"}, false),
				Description:  "Front-end protocol. One of: HTTP, HTTPS, TCP, UDP.",
			},
			"protocol_port": {
				Type:         schema.TypeInt,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.IsPortNumber,
				Description:  "Front-end port the listener accepts traffic on.",
			},
			"name": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Name of the listener.",
			},
			"description": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Free-text description.",
			},
			"admin_state_up": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     true,
				Description: "Whether the listener is administratively up.",
			},
			"connection_limit": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Maximum number of concurrent connections.",
			},
			"default_pool_id": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Pool that receives traffic when no other rule matches.",
			},
			"allowed_cidrs": {
				Type:        schema.TypeList,
				Optional:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "CIDRs permitted to reach this listener.",
			},
			"timeout_client_data": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Client inactivity timeout in milliseconds.",
			},
			"timeout_member_connect": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Member connect timeout in milliseconds.",
			},
			"timeout_member_data": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Member inactivity timeout in milliseconds.",
			},
			"insert_headers": insertHeadersSchema(false),

			"listener_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "ID of the listener on its own, without the load balancer prefix.",
			},
			"provisioning_status": {Type: schema.TypeString, Computed: true, Description: "Provisioning status."},
			"operating_status":    {Type: schema.TypeString, Computed: true, Description: "Operating status."},
			"created_at":          {Type: schema.TypeString, Computed: true, Description: "Creation timestamp as reported by the API."},
			"updated_at":          {Type: schema.TypeString, Computed: true, Description: "Last-update timestamp as reported by the API."},
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(15 * time.Minute),
			Update: schema.DefaultTimeout(15 * time.Minute),
			Delete: schema.DefaultTimeout(15 * time.Minute),
		},
	}
}

func resourceDtcloudLBListenerCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)

	// The create endpoint does not report which listener it made, so the new one is
	// identified by diffing the list before and after.
	before, _, err := client.LoadBalancer.ListLoadBalancerListeners(ctx, lbID, nil)
	if err != nil {
		return diag.Errorf("Error listing listeners on load balancer %q: %s", lbID, err)
	}
	known := make(map[string]bool, len(before))
	for _, l := range before {
		known[l.ID] = true
	}

	opts := dtgo.CreateListenerParams{
		Name:           d.Get("name").(string),
		Description:    d.Get("description").(string),
		LBProtocol:     d.Get("protocol").(string),
		LBProtocolPort: d.Get("protocol_port").(int),
	}
	if err := mutateChild(ctx, client, lbID, d.Timeout(schema.TimeoutCreate), func() error {
		_, err := client.LoadBalancer.CreateListener(ctx, lbID, opts, nil)
		return err
	}); err != nil {
		return diag.Errorf("Error creating listener on load balancer %q: %s", lbID, err)
	}

	var listenerID string
	err = waitForCondition(ctx, d.Timeout(schema.TimeoutCreate), func() (bool, error) {
		listeners, _, err := client.LoadBalancer.ListLoadBalancerListeners(ctx, lbID, nil)
		if err != nil {
			return false, err
		}
		for _, l := range listeners {
			if l.ID != "" && !known[l.ID] {
				listenerID = l.ID
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return diag.Errorf("Error waiting for the new listener on load balancer %q: %s", lbID, err)
	}

	d.SetId(compositeID(lbID, listenerID))
	d.Set("listener_id", listenerID)

	// Everything beyond protocol/port is only settable through update.
	if diags := applyListenerUpdate(ctx, d, meta); diags.HasError() {
		return diags
	}

	return resourceDtcloudLBListenerRead(ctx, d, meta)
}

func resourceDtcloudLBListenerRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)
	listenerID := d.Get("listener_id").(string)

	listeners, _, err := client.LoadBalancer.ListLoadBalancerListeners(ctx, lbID, nil)
	if err != nil {
		// The load balancer being gone takes its listeners with it.
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error listing listeners on load balancer %q: %s", lbID, err)
	}

	for _, l := range listeners {
		if l.ID != listenerID {
			continue
		}
		d.Set("name", l.Name)
		d.Set("description", l.Description)
		d.Set("protocol", l.Protocol)
		d.Set("protocol_port", l.ProtocolPort)
		d.Set("admin_state_up", l.AdminStateUp)
		d.Set("connection_limit", l.ConnectionLimit)
		d.Set("default_pool_id", l.DefaultPoolID)
		d.Set("allowed_cidrs", l.AllowedCidrs)
		d.Set("timeout_client_data", l.TimeoutClientData)
		d.Set("timeout_member_connect", l.TimeoutMemberConnect)
		d.Set("timeout_member_data", l.TimeoutMemberData)
		d.Set("insert_headers", flattenInsertHeaders(l.InsertHeaders))
		d.Set("provisioning_status", l.ProvisioningStatus)
		d.Set("operating_status", l.OperatingStatus)
		d.Set("created_at", l.CreatedAt)
		d.Set("updated_at", l.UpdatedAt)
		return nil
	}

	// Deleted outside Terraform.
	d.SetId("")
	return nil
}

func resourceDtcloudLBListenerUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	if diags := applyListenerUpdate(ctx, d, meta); diags.HasError() {
		return diags
	}
	return resourceDtcloudLBListenerRead(ctx, d, meta)
}

// applyListenerUpdate pushes the mutable fields, shared by Create and Update
// because create accepts only protocol, port, name and description.
func applyListenerUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)
	listenerID := d.Get("listener_id").(string)

	opts := dtgo.UpdateLoadBalancerListenerParams{
		Name:          d.Get("name").(string),
		Description:   d.Get("description").(string),
		AdminStateUp:  boolPtr(d.Get("admin_state_up").(bool)),
		DefaultPoolID: bareID(d.Get("default_pool_id").(string)),
		AllowedCIDRs:  expandStringList(d.Get("allowed_cidrs").([]interface{})),
		InsertHeaders: expandInsertHeaders(d.Get("insert_headers").([]interface{})),
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

	if err := mutateChild(ctx, client, lbID, d.Timeout(schema.TimeoutUpdate), func() error {
		_, err := client.LoadBalancer.UpdateLoadBalancerListener(ctx, lbID, listenerID, opts, nil)
		return err
	}); err != nil {
		return diag.Errorf("Error updating listener %q: %s", listenerID, err)
	}
	if err := waitForLBActive(ctx, client, lbID, d.Timeout(schema.TimeoutUpdate)); err != nil {
		return diag.Errorf("Error waiting for load balancer %q after updating listener %q: %s", lbID, listenerID, err)
	}
	return nil
}

func resourceDtcloudLBListenerDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	lbID := d.Get("lb_id").(string)
	listenerID := d.Get("listener_id").(string)

	if err := mutateChild(ctx, client, lbID, d.Timeout(schema.TimeoutDelete), func() error {
		_, err := client.LoadBalancer.DeleteLoadBalancerListener(ctx, lbID, listenerID, nil)
		if dterr.IsNotFound(err) {
			return nil
		}
		return err
	}); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error deleting listener %q: %s", listenerID, err)
		}
	}

	err := waitForCondition(ctx, d.Timeout(schema.TimeoutDelete), func() (bool, error) {
		listeners, _, err := client.LoadBalancer.ListLoadBalancerListeners(ctx, lbID, nil)
		if err != nil {
			if dterr.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		for _, l := range listeners {
			if l.ID == listenerID {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return diag.Errorf("Error waiting for listener %q to be deleted: %s", listenerID, err)
	}

	d.SetId("")
	return nil
}

// importLBChild builds an importer for a resource addressed by a composite id.
// The field names are given in the same order as the id's parts.
func importLBChild(shape string, fields ...string) schema.StateContextFunc {
	return func(ctx context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
		parts, err := splitID(d.Id(), len(fields), shape)
		if err != nil {
			return nil, err
		}
		for i, f := range fields {
			d.Set(f, parts[i])
		}
		d.SetId(compositeID(parts...))
		return []*schema.ResourceData{d}, nil
	}
}
