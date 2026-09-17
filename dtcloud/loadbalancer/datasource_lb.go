package loadbalancer

import (
	"context"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// DataSourceDtcloudLB looks up a single load balancer by id, since names are
// not unique.
func DataSourceDtcloudLB() *schema.Resource {
	return &schema.Resource{
		Description: "Looks up one load balancer by ID.",

		ReadContext: dataSourceDtcloudLBRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the load balancer.",
			},
			"name":              {Type: schema.TypeString, Computed: true, Description: "Name of the load balancer."},
			"description":       {Type: schema.TypeString, Computed: true, Description: "Free-text description."},
			"status":            {Type: schema.TypeString, Computed: true, Description: "Provisioning status."},
			"floating_ip":       {Type: schema.TypeString, Computed: true, Description: "Floating IP, when one is associated."},
			"flavor_name":       {Type: schema.TypeString, Computed: true, Description: "Human-readable flavor name."},
			"high_availability": {Type: schema.TypeString, Computed: true, Description: "High-availability mode."},
			"balancing_pools":   {Type: schema.TypeInt, Computed: true, Description: "Number of balancing pools."},
			"members_total":     {Type: schema.TypeInt, Computed: true, Description: "Total number of members."},
			"created_on":        {Type: schema.TypeString, Computed: true, Description: "Creation timestamp as reported by the API."},
			"members_state": {
				Type:        schema.TypeList,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "Operating state of each member.",
			},
			"network": lbNetworkSchema(),
		},
	}
}

func dataSourceDtcloudLBRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	id := d.Get("id").(string)

	details, _, err := client.LoadBalancer.GetLoadBalancerDetails(ctx, id, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("Load balancer %q not found", id)
		}
		return diag.Errorf("Error retrieving load balancer %q: %s", id, err)
	}
	if details.ID == "" {
		return diag.Errorf("Load balancer %q not found", id)
	}

	d.SetId(details.ID)
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

	return nil
}
