package elasticip

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// DataSourceDtcloudElasticIPs lists the elastic IPs allocated to the caller.
//
// The most useful thing it answers is "what are we paying for that nothing is
// using" — an allocated address that is not associated still occupies quota and
// is still billed. `status = "DOWN"` is that question.
//
// Filters are applied here rather than by the API: the list route takes no
// query parameters. Every address is fetched either way, and the API builds
// each row from three further calls of its own, so this is not a cheap read —
// prefer the singular data source when you already know which address you want.
func DataSourceDtcloudElasticIPs() *schema.Resource {
	return &schema.Resource{
		Description: "Lists the elastic IPs allocated to the caller.\n\n" +
			"The most useful thing it answers is what you are paying for that nothing is using: an " +
			"allocated address that is not associated still occupies quota and is still billed. `status = " +
			"\"DOWN\"` is that question.\n\n" +
			"Filters are applied by the provider, since the list route takes no query parameters. Every " +
			"address is fetched either way and the API builds each row from three further calls of its " +
			"own, so this is not a cheap read -- prefer `dtcloud_elastic_ip` when you already know which " +
			"address you want.",

		ReadContext: dataSourceDtcloudElasticIPsRead,
		Schema: map[string]*schema.Schema{
			"status": {
				Type:         schema.TypeString,
				Optional:     true,
				ValidateFunc: validation.StringInSlice([]string{statusActive, statusDown, statusError}, false),
				Description:  "Only return addresses in this status: `ACTIVE`, `DOWN` or `ERROR`.",
			},
			"floating_network_id": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return addresses allocated from this external network.",
			},
			"assigned_type": {
				Type:         schema.TypeString,
				Optional:     true,
				ValidateFunc: validation.StringInSlice([]string{"VM", "LB", "none"}, false),
				Description:  "Only return addresses attached to a `VM`, to an `LB`, or attached to nothing (`none`).",
			},

			"elastic_ips": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The addresses that matched.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":                  {Type: schema.TypeString, Computed: true, Description: "ID of the elastic IP."},
						"ip_address":          {Type: schema.TypeString, Computed: true, Description: "The public address."},
						"status":              {Type: schema.TypeString, Computed: true, Description: "`ACTIVE` when associated, `DOWN` when not."},
						"floating_network_id": {Type: schema.TypeString, Computed: true, Description: "External network it came from."},
						"network_name":        {Type: schema.TypeString, Computed: true, Description: "Name of that network."},
						"assigned_type":       {Type: schema.TypeString, Computed: true, Description: "`VM`, `LB`, or empty."},
						"assigned_id":         {Type: schema.TypeString, Computed: true, Description: "ID of the VM or load balancer."},
						"assigned_to":         {Type: schema.TypeString, Computed: true, Description: "Name of the VM or load balancer."},
						"fixed_ip_address":    {Type: schema.TypeString, Computed: true, Description: "Private address the traffic is mapped to."},
					},
				},
			},
			"ids": {
				Type:        schema.TypeList,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "Just the ids of the addresses that matched.",
			},
			"ip_addresses": {
				Type:        schema.TypeList,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "Just the addresses, in the same order as `ids`.",
			},
		},
	}
}

func dataSourceDtcloudElasticIPsRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	rows, _, err := client.FloatingIps.List(ctx, nil)
	if err != nil {
		return diag.Errorf("Error listing elastic IPs: %s", err)
	}

	statusFilter := d.Get("status").(string)
	networkFilter := d.Get("floating_network_id").(string)
	assignedFilter := d.Get("assigned_type").(string)

	out := make([]interface{}, 0, len(rows))
	ids := make([]interface{}, 0, len(rows))
	addresses := make([]interface{}, 0, len(rows))
	flat := make([]string, 0, len(rows))

	for _, row := range rows {
		if statusFilter != "" && row.Status != statusFilter {
			continue
		}
		if networkFilter != "" && row.NetworkID != networkFilter {
			continue
		}
		// The API reports "attached to nothing" as an empty string, which is
		// not a value anyone can type into a filter, so "none" stands in.
		if assignedFilter != "" {
			want := assignedFilter
			if want == "none" {
				want = ""
			}
			if row.VMOrLB != want {
				continue
			}
		}

		out = append(out, map[string]interface{}{
			"id":                  row.ID,
			"ip_address":          row.IPAddress,
			"status":              row.Status,
			"floating_network_id": row.NetworkID,
			"network_name":        row.Network,
			"assigned_type":       row.VMOrLB,
			"assigned_id":         row.AssignedID,
			"assigned_to":         row.AssignedTo,
			// Null for an unassociated address, so a pointer in the SDK.
			"fixed_ip_address": derefAny(row.VMIp),
		})
		ids = append(ids, row.ID)
		addresses = append(addresses, row.IPAddress)
		flat = append(flat, row.ID)
	}

	if err := d.Set("elastic_ips", out); err != nil {
		return diag.FromErr(err)
	}
	if err := d.Set("ids", ids); err != nil {
		return diag.FromErr(err)
	}
	if err := d.Set("ip_addresses", addresses); err != nil {
		return diag.FromErr(err)
	}

	// A stable id keeps an unchanged result from showing up as a diff.
	sum := sha256.Sum256([]byte(strings.Join(flat, ",")))
	d.SetId(fmt.Sprintf("%x", sum[:8]))

	return nil
}

// derefAny is deref for the list route, whose vmIpAddress is null on every
// unassociated address.
func derefAny(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
