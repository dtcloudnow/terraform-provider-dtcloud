package securitygroup

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudMyIP reports the address the API sees the caller coming from
// — their address on whatever network they reached it over, not their public one.
//
// The value changes with the caller's network and a rule is entirely ForceNew,
// so whichever machine ran the last apply decides who has access.
func DataSourceDtcloudMyIP() *schema.Resource {
	return &schema.Resource{
		Description: "Reports the address the API sees the caller coming from.",

		ReadContext: dataSourceDtcloudMyIPRead,
		Schema: map[string]*schema.Schema{
			"ip": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The address the API saw the request come from.",
			},
			"cidr": {
				Type:     schema.TypeString,
				Computed: true,
				Description: "The same address as a single-host CIDR — `/32` for IPv4, `/128` for IPv6 — " +
					"which is the form `remote_ip_prefix` takes.",
			},
		},
	}
}

func dataSourceDtcloudMyIPRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	body, err := client.SecurityGroup.GetMyIP(ctx, nil)
	if err != nil {
		return diag.Errorf("Error retrieving your public IP: %s", err)
	}

	// dt-go hands this one back as a raw body; the route answers {"ip": "..."}.
	var parsed struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return diag.Errorf("Error reading the address from the API response: %s (body: %s)", err, body)
	}
	if parsed.IP == "" {
		return diag.Errorf("The API returned no address (body: %s)", body)
	}

	// An IPv4 address reached over a dual-stack listener is reported in the
	// IPv4-mapped form ::ffff:1.2.3.4, which is not a usable prefix on its own.
	ip := net.ParseIP(parsed.IP)
	if ip == nil {
		return diag.Errorf("The API returned %q, which is not an IP address", parsed.IP)
	}
	prefix := fmt.Sprintf("%s/128", ip.String())
	if v4 := ip.To4(); v4 != nil {
		ip = v4
		prefix = fmt.Sprintf("%s/32", v4.String())
	}

	d.SetId(ip.String())
	d.Set("ip", ip.String())
	d.Set("cidr", prefix)

	return nil
}
