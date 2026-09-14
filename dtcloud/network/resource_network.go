package network

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudNetwork manages a network and, when IPAM is on, its subnet.
//
// In place: `name`, and the subnet's `gateway_ip`, `enable_dhcp`,
// `dns_nameservers` and `allocation_pools`. `ipam_enabled` and `cidr` are
// ForceNew.
//
// The subnet update takes all four fields at once, so Update sends the complete
// set and Read has to recover every one — a field it missed would go back empty
// on the next unrelated change.
func ResourceDtcloudNetwork() *schema.Resource {
	return &schema.Resource{
		Description: "Manages a network and, when IPAM is enabled, the subnet inside it.",

		CreateContext: resourceDtcloudNetworkCreate,
		ReadContext:   resourceDtcloudNetworkRead,
		UpdateContext: resourceDtcloudNetworkUpdate,
		DeleteContext: resourceDtcloudNetworkDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "Name of the network. Can be changed in place.",
			},

			// One flag, two effects — see the package comment.
			"ipam_enabled": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  true,
				ForceNew: true,
				Description: "Whether the platform manages addressing on this network. " +
					"With it on, a subnet is created from `cidr` and port security is enabled. " +
					"With it off there is no subnet at all: no CIDR, no gateway, no DHCP.",
			},
			"cidr": {
				Type:         schema.TypeString,
				Optional:     true,
				ForceNew:     true,
				ValidateFunc: validation.IsCIDR,
				Description:  "IPv4 range for the subnet, e.g. 10.0.0.0/24. Required when ipam_enabled is true, and rejected when it is false.",
			},

			// Subnet settings. All updatable, all meaningless without IPAM.
			"gateway_ip": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ValidateFunc: validation.IsIPv4Address,
				Description:  "Gateway address. The platform picks the first usable address when omitted.",
			},
			"enable_dhcp": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     true,
				Description: "Whether the subnet hands out addresses over DHCP.",
			},
			"dns_nameservers": {
				Type:        schema.TypeList,
				Optional:    true,
				Elem:        &schema.Schema{Type: schema.TypeString, ValidateFunc: validation.IsIPAddress},
				Description: "DNS servers advertised to instances on this network.",
			},
			"allocation_pools": allocationPoolSchema(),

			"subnet_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "ID of the subnet, empty when ipam_enabled is false.",
			},
			"network_type": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Virtual or Physical, as reported by the platform.",
			},
			"ip_version": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "IP version of the subnet. The API creates IPv4 only.",
			},
		},

		// The two rules the schema cannot express, caught here so that they are
		// plan-time errors rather than a 500 from the API.
		CustomizeDiff: func(ctx context.Context, d *schema.ResourceDiff, meta interface{}) error {
			ipam := d.Get("ipam_enabled").(bool)
			cidr := d.Get("cidr").(string)

			if ipam && cidr == "" {
				return fmt.Errorf("cidr is required when ipam_enabled is true: the subnet has no address range to allocate from")
			}
			if !ipam {
				if cidr != "" {
					return fmt.Errorf("cidr must not be set when ipam_enabled is false: no subnet is created, so there is nothing to apply it to")
				}
				if !d.Get("enable_dhcp").(bool) {
					return fmt.Errorf("enable_dhcp must not be set when ipam_enabled is false: there is no subnet to configure")
				}
				for _, field := range []string{"gateway_ip", "dns_nameservers", "allocation_pools"} {
					if v, ok := d.GetOk(field); ok {
						if list, isList := v.([]interface{}); isList && len(list) == 0 {
							continue
						}
						return fmt.Errorf("%s must not be set when ipam_enabled is false: there is no subnet to configure", field)
					}
				}
			}
			return nil
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(15 * time.Minute),
			Update: schema.DefaultTimeout(10 * time.Minute),
			Delete: schema.DefaultTimeout(15 * time.Minute),
		},
	}
}

// createdNetworkID digs the new network's id out of the create response, which
// has two shapes: `network_id` inside a wrapper with IPAM on, `id` on a bare
// object with it off. The bare one is read from the raw body.
func createdNetworkID(resp *dtgo.CreateNetworkResponse, body string) (string, error) {
	if resp != nil {
		if resp.Subnet != nil && resp.Subnet.NetworkID != "" {
			return resp.Subnet.NetworkID, nil
		}
		if resp.Network != nil && resp.Network.ID != "" {
			return resp.Network.ID, nil
		}
	}

	var bare struct {
		ID      string `json:"id"`
		Network struct {
			ID string `json:"id"`
		} `json:"network"`
		Subnet struct {
			NetworkID string `json:"network_id"`
		} `json:"subnet"`
	}
	if err := json.Unmarshal([]byte(body), &bare); err != nil {
		return "", fmt.Errorf("the API response was not a JSON object: %w (body: %s)", err, body)
	}
	for _, candidate := range []string{bare.Subnet.NetworkID, bare.Network.ID, bare.ID} {
		if candidate != "" {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no network id in the API response (body: %s)", body)
}

func resourceDtcloudNetworkCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	req := dtgo.CreateNetworkRequest{
		IPAM: d.Get("ipam_enabled").(bool),
		Name: d.Get("name").(string),
	}
	if req.IPAM {
		req.CIDR = d.Get("cidr").(string)
		req.EnableDHCP = d.Get("enable_dhcp").(bool)
		req.GatewayIP = d.Get("gateway_ip").(string)
		req.DNSNameservers = expandStringList(d.Get("dns_nameservers").([]interface{}))
		req.AllocationPools = expandAllocationPools(d.Get("allocation_pools").([]interface{}))
		// The route fixes ip_version at 4 regardless of what is sent, so there is
		// no argument exposed for it.
		req.IPVersion = 4
	}

	resp, body, err := client.Network.CreateNetwork(ctx, req)
	if err != nil {
		return diag.Errorf("Error creating network: %s", err)
	}

	id, err := createdNetworkID(resp, body)
	if err != nil {
		return diag.Errorf("Error reading the created network's id: %s", err)
	}
	d.SetId(id)

	if err := waitForNetwork(ctx, client, id, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.Errorf("Error waiting for network %q to become readable: %s", id, err)
	}

	return resourceDtcloudNetworkRead(ctx, d, meta)
}

func resourceDtcloudNetworkRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	details, _, err := client.Network.GetNetworkDetails(ctx, d.Id())
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error retrieving network %q: %s", d.Id(), err)
	}
	if details.NetworkConfiguration.ID == "" {
		d.SetId("")
		return nil
	}

	d.Set("name", details.NetworkConfiguration.Name)
	d.Set("network_type", details.NetworkConfiguration.Type)

	// A network without IPAM has no subnet, and the details endpoint omits the
	// whole object, so everything subnet-shaped stays at its zero value.
	if details.Subnets == nil {
		d.Set("ipam_enabled", false)
		d.Set("subnet_id", "")
		// enable_dhcp defaults to true in the schema and there is nothing to read
		// it from without a subnet. Left unset, an import lands on false and
		// applying that diff reaches the subnet update with no subnet.
		d.Set("enable_dhcp", true)
		return nil
	}

	d.Set("ipam_enabled", true)
	d.Set("subnet_id", details.Subnets.ID)
	d.Set("ip_version", details.Subnets.SubnetIPVersion)
	d.Set("cidr", details.Subnets.CIDR)
	d.Set("gateway_ip", details.Subnets.Gateway)
	d.Set("enable_dhcp", details.Subnets.DHCP)
	d.Set("dns_nameservers", details.Subnets.DNSServer)
	d.Set("allocation_pools", flattenAllocationPools(details.Subnets.AllocationPools))

	return nil
}

func resourceDtcloudNetworkUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	if d.HasChange("name") {
		if _, _, err := client.Network.UpdateNetwork(ctx, d.Id(), d.Get("name").(string), nil); err != nil {
			return diag.Errorf("Error renaming network %q: %s", d.Id(), err)
		}
	}

	if d.HasChanges("gateway_ip", "enable_dhcp", "dns_nameservers", "allocation_pools") {
		subnetID := d.Get("subnet_id").(string)
		if subnetID == "" {
			// Unreachable through the schema, but a clear error beats a nil
			// dereference if that ever changes.
			return diag.Errorf("Network %q has no subnet to update; subnet settings need ipam_enabled = true", d.Id())
		}

		// The whole set goes every time: the endpoint requires all of them, and
		// the two arrays must be arrays rather than null.
		params := dtgo.UpdateSubnetParams{
			EnableDHCP:      d.Get("enable_dhcp").(bool),
			GatewayIP:       d.Get("gateway_ip").(string),
			DNSNameservers:  expandStringList(d.Get("dns_nameservers").([]interface{})),
			AllocationPools: expandAllocationPools(d.Get("allocation_pools").([]interface{})),
		}
		if _, _, err := client.Network.UpdateSubnet(ctx, d.Id(), subnetID, params, nil); err != nil {
			return diag.Errorf("Error updating subnet %q on network %q: %s", subnetID, d.Id(), err)
		}
	}

	return resourceDtcloudNetworkRead(ctx, d, meta)
}

func resourceDtcloudNetworkDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	if _, err := client.Network.DeleteNetwork(ctx, d.Id(), nil); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error deleting network %q: %s", d.Id(), err)
		}
	}

	if err := waitForNetworkGone(ctx, client, d.Id(), d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.Errorf("Error waiting for network %q to be deleted: %s", d.Id(), err)
	}

	d.SetId("")
	return nil
}
