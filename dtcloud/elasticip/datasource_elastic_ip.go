package elasticip

import (
	"context"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudElasticIP looks up one elastic IP, by id or by address. It
// costs two calls: `details` carries the raw fields, the list carries the
// external network's name and what the address is attached to.
func DataSourceDtcloudElasticIP() *schema.Resource {
	return &schema.Resource{
		Description: "Looks up one elastic IP, by id or by address.",

		ReadContext: dataSourceDtcloudElasticIPRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ExactlyOneOf: []string{"id", "ip_address"},
				Description:  "ID of the elastic IP to look up. Give this or `ip_address`.",
			},
			"ip_address": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ExactlyOneOf: []string{"id", "ip_address"},
				Description:  "Public address to look up. Give this or `id`. Addresses are unique.",
			},

			"floating_network_id": {Type: schema.TypeString, Computed: true, Description: "ID of the external network the address came from."},
			"network_name":        {Type: schema.TypeString, Computed: true, Description: "Name of that external network, as the platform reports it."},
			"status":              {Type: schema.TypeString, Computed: true, Description: "`ACTIVE` when associated, `DOWN` when not."},
			"port_id":             {Type: schema.TypeString, Computed: true, Description: "Port the address is pointed at, empty when unassociated."},
			"fixed_ip_address":    {Type: schema.TypeString, Computed: true, Description: "Address on that port the traffic is mapped to."},
			"device_id":           {Type: schema.TypeString, Computed: true, Description: "ID of the resource behind the port."},
			"device_owner":        {Type: schema.TypeString, Computed: true, Description: "What kind of thing is behind the port, e.g. `compute:nova`."},
			"assigned_id":         {Type: schema.TypeString, Computed: true, Description: "ID of the VM or load balancer the platform reports the address as assigned to."},
			"assigned_to":         {Type: schema.TypeString, Computed: true, Description: "Name of that VM or load balancer."},
			"assigned_type":       {Type: schema.TypeString, Computed: true, Description: "`VM`, `LB`, or empty when the address is unassociated."},
			"router_id":           {Type: schema.TypeString, Computed: true, Description: "Router carrying the traffic."},
			"description":         {Type: schema.TypeString, Computed: true, Description: "Description of the address."},
			"created_at":          {Type: schema.TypeString, Computed: true, Description: "When the address was allocated, in RFC 3339 format."},
			"updated_at":          {Type: schema.TypeString, Computed: true, Description: "When it was last changed, in RFC 3339 format."},
		},
	}
}

func dataSourceDtcloudElasticIPRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	id := d.Get("id").(string)
	address := d.Get("ip_address").(string)

	// The list is needed either way: to resolve an address to an id, and for
	// the cooked fields that only appear there.
	rows, _, err := client.FloatingIps.List(ctx, nil)
	if err != nil {
		return diag.Errorf("Error listing elastic IPs: %s", err)
	}

	matched := -1
	for i, row := range rows {
		if (id != "" && row.ID == id) || (address != "" && row.IPAddress == address) {
			matched = i
			break
		}
	}
	if matched < 0 {
		if id != "" {
			return diag.Errorf("Elastic IP %q not found", id)
		}
		return diag.Errorf("No elastic IP with the address %q", address)
	}
	row := rows[matched]

	details, body, err := client.FloatingIps.GetDetails(ctx, row.ID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("Elastic IP %q not found", row.ID)
		}
		return diag.Errorf("Error retrieving elastic IP %q: %s", row.ID, err)
	}
	if details == nil || details.ID == "" {
		return diag.Errorf("Elastic IP %q not found", row.ID)
	}

	d.SetId(details.ID)
	d.Set("id", details.ID)
	d.Set("ip_address", details.FloatingIPAddress)
	d.Set("floating_network_id", details.FloatingNetworkID)
	d.Set("network_name", row.Network)
	d.Set("status", details.Status)
	d.Set("port_id", deref(details.PortID))
	d.Set("fixed_ip_address", deref(details.FixedIPAddress))
	d.Set("router_id", deref(details.RouterID))
	d.Set("description", details.Description)
	d.Set("created_at", formatTime(details.CreatedAt))
	d.Set("updated_at", formatTime(details.UpdatedAt))

	pd := parsePortDetails(body)
	d.Set("device_id", pd.DeviceID)
	d.Set("device_owner", pd.DeviceOwner)

	d.Set("assigned_id", row.AssignedID)
	d.Set("assigned_to", row.AssignedTo)
	d.Set("assigned_type", row.VMOrLB)

	return nil
}
