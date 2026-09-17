package loadbalancer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// DataSourceDtcloudLBs lists the load balancers in the active region.
func DataSourceDtcloudLBs() *schema.Resource {
	return &schema.Resource{
		Description: "Lists the load balancers in the active region.",

		ReadContext: dataSourceDtcloudLBsRead,
		Schema: map[string]*schema.Schema{
			"load_balancers": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The load balancers visible to the caller.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":            {Type: schema.TypeString, Computed: true},
						"name":          {Type: schema.TypeString, Computed: true},
						"status":        {Type: schema.TypeString, Computed: true},
						"ip_address":    {Type: schema.TypeString, Computed: true},
						"floating_ip":   {Type: schema.TypeString, Computed: true},
						"members_total": {Type: schema.TypeInt, Computed: true},
						"port_id":       {Type: schema.TypeString, Computed: true},
						"flavor_name":   {Type: schema.TypeString, Computed: true},
						"network":       {Type: schema.TypeList, Computed: true, Elem: &schema.Resource{Schema: map[string]*schema.Schema{"id": {Type: schema.TypeString, Computed: true}, "name": {Type: schema.TypeString, Computed: true}, "ip": {Type: schema.TypeString, Computed: true}}}},
					},
				},
			},
		},
	}
}

func dataSourceDtcloudLBsRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	lbs, _, err := client.LoadBalancer.ListLoadBalancers(ctx, nil)
	if err != nil {
		return diag.Errorf("Error listing load balancers: %s", err)
	}

	out := make([]interface{}, 0, len(lbs))
	ids := make([]string, 0, len(lbs))
	for _, lb := range lbs {
		out = append(out, map[string]interface{}{
			"id":            lb.ID,
			"name":          lb.Name,
			"status":        lb.Status,
			"ip_address":    lb.IPAddress,
			"floating_ip":   lb.FloatingIP,
			"members_total": lb.MembersTotal,
			"port_id":       lb.PortID,
			"flavor_name":   lb.FlavorName,
			"network":       flattenLBNetwork(lb.Network),
		})
		ids = append(ids, lb.ID)
	}

	if err := d.Set("load_balancers", out); err != nil {
		return diag.FromErr(err)
	}
	d.SetId(hashIDs(ids))

	return nil
}

// DataSourceDtcloudLBVms lists the VMs on a network that can be added as
// members, for building a `dtcloud_lb_member` set with `for_each`.
func DataSourceDtcloudLBVms() *schema.Resource {
	return &schema.Resource{
		Description: "Lists the machines on a network that can be added to a pool as members.",

		ReadContext: dataSourceDtcloudLBVmsRead,
		Schema: map[string]*schema.Schema{
			"network_id": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "Network to look for candidate members on.",
			},
			"pool_id": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "When set, VMs already in this pool are left out of the result.",
			},
			"vms": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "VMs attached to the network.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":   {Type: schema.TypeString, Computed: true, Description: "ID of the VM, for use as compute_server_id."},
						"name": {Type: schema.TypeString, Computed: true},
						"ip_addresses": {
							Type:        schema.TypeList,
							Computed:    true,
							Elem:        &schema.Schema{Type: schema.TypeString},
							Description: "Every address the VM holds on this network.",
						},
						"ip_address": {
							Type:        schema.TypeString,
							Computed:    true,
							Description: "First address from ip_addresses, for the common case of a VM with one address on the network.",
						},
					},
				},
			},
		},
	}
}

// lbCandidateVM is the shape the candidate lookup returns. The addresses come
// back as a list: a VM with several addresses on one network is ordinary.
type lbCandidateVM struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	IPs  []string `json:"ips"`
}

func (v lbCandidateVM) address() string {
	if len(v.IPs) == 0 {
		return ""
	}
	return v.IPs[0]
}

// lbCandidateVMOptions is the query string for the candidate lookup.
type lbCandidateVMOptions struct {
	PoolID string `url:"poolId,omitempty"`
}

func dataSourceDtcloudLBVmsRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	networkID := d.Get("network_id").(string)

	var opt interface{}
	if poolID, ok := d.GetOk("pool_id"); ok {
		opt = lbCandidateVMOptions{PoolID: poolID.(string)}
	}

	body, err := client.LoadBalancer.ListLoadBalancerVmsByNetwork(ctx, networkID, opt)
	if err != nil {
		return diag.Errorf("Error listing candidate members on network %q: %s", networkID, err)
	}

	var vms []lbCandidateVM
	if err := json.Unmarshal([]byte(body), &vms); err != nil {
		// Some endpoints wrap the array; try the common shape before giving up.
		var wrapped struct {
			VMs []lbCandidateVM `json:"vms"`
		}
		if err2 := json.Unmarshal([]byte(body), &wrapped); err2 != nil {
			return diag.Errorf("Error decoding the candidate member list for network %q: %s (body: %s)", networkID, err, body)
		}
		vms = wrapped.VMs
	}

	out := make([]interface{}, 0, len(vms))
	ids := make([]string, 0, len(vms))
	for _, v := range vms {
		addresses := make([]interface{}, 0, len(v.IPs))
		for _, ip := range v.IPs {
			addresses = append(addresses, ip)
		}
		out = append(out, map[string]interface{}{
			"id":           v.ID,
			"name":         v.Name,
			"ip_addresses": addresses,
			"ip_address":   v.address(),
		})
		ids = append(ids, v.ID)
	}

	if err := d.Set("vms", out); err != nil {
		return diag.FromErr(err)
	}
	d.SetId(hashIDs(append([]string{networkID}, ids...)))

	return nil
}

// hashIDs gives a data source a stable id, so unchanged results are not a diff.
func hashIDs(ids []string) string {
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	return fmt.Sprintf("%x", sum[:8])
}
