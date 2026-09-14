package router

import (
	"context"
	"fmt"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudRouterInterface attaches a private network to a router.
//
// The resource id is `<router-id>:<port-id>`. The port id is assigned by the
// platform at attach time and the attach endpoint does not report it, so it is
// found by comparing the router's interface list before and after the call.
//
// Nothing here can be changed in place: the API has an attach endpoint and a
// detach endpoint and nothing in between, so every argument is ForceNew and the
// resource has no Update.
func ResourceDtcloudRouterInterface() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceDtcloudRouterInterfaceCreate,
		ReadContext:   resourceDtcloudRouterInterfaceRead,
		DeleteContext: resourceDtcloudRouterInterfaceDelete,
		Importer: &schema.ResourceImporter{
			StateContext: resourceDtcloudRouterInterfaceImport,
		},

		Schema: map[string]*schema.Schema{
			"router_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the router to attach the network to.",
			},
			"network_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the network to attach.",
			},
			// Asking for an address and letting the platform choose one are
			// two different code paths on the API's side, not one call with an
			// optional field. See the comment on attachInterfaceParams.
			"ip_address": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ForceNew:     true,
				ValidateFunc: validation.IsIPAddress,
				Description: "Address the interface takes on the network. When omitted, the " +
					"platform attaches to the network's first subnet and allocates an address " +
					"from it.",
			},
			"port_security_enabled": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  true,
				ForceNew: true,
				Description: "Whether port security applies to this interface. It only reaches " +
					"the platform when `ip_address` is set; attaching without one goes through " +
					"a path that does not take the setting.",
			},

			"port_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "ID of the port the platform created for this interface.",
			},
			"subnet_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "ID of the subnet the interface's address came from.",
			},
			"network_name": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Name of the attached network.",
			},
			"cidr": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "CIDR of the attached network's first subnet.",
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Status of the interface's port.",
			},
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(10 * time.Minute),
			Delete: schema.DefaultTimeout(10 * time.Minute),
		},
	}
}

func routerInterfaceID(routerID, portID string) string {
	return fmt.Sprintf("%s:%s", routerID, portID)
}

// attachInterfaceParams builds the attach request.
//
// The endpoint branches on the first fixed IP: an entry that declares IPv4
// makes it attach the network's first subnet and discard any address given
// alongside, while an entry without a version makes it create a port carrying
// exactly the address asked for. So requesting an address means sending the
// address alone, and letting the platform choose means sending the version
// alone. The two are not the same call with a field left out.
func attachInterfaceParams(networkID, ipAddress string, portSecurity bool) dtgo.AttachInterfaceToRouterParams {
	fixedIP := dtgo.FixedIP{IPVersion: 4}
	if ipAddress != "" {
		fixedIP = dtgo.FixedIP{IPAddress: ipAddress}
	}
	return dtgo.AttachInterfaceToRouterParams{
		NetworkId:           networkID,
		PortSecurityEnabled: portSecurity,
		// Never nil: the endpoint validates this with an array rule that
		// rejects null outright.
		FixedIPs: []dtgo.FixedIP{fixedIP},
	}
}

// internalPortIDs lists the ports of a router's internal interfaces. The
// external gateway is excluded because its entry reports a subnet id in the
// same field, which is not something detaching can be asked for.
func internalPortIDs(interfaces dtgo.ListRouterInterfaces) map[string]bool {
	ports := map[string]bool{}
	for _, iface := range interfaces {
		if iface.Type == internalInterfaceType && iface.ID != "" {
			ports[iface.ID] = true
		}
	}
	return ports
}

func resourceDtcloudRouterInterfaceCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	routerID := d.Get("router_id").(string)
	networkID := d.Get("network_id").(string)

	// Record the ports that already exist so the new one can be identified.
	before, _, err := client.Router.ListRouterInterfaces(ctx, routerID, nil)
	if err != nil {
		return diag.Errorf("Error listing the interfaces of router %q: %s", routerID, err)
	}
	known := internalPortIDs(before)

	params := attachInterfaceParams(networkID, d.Get("ip_address").(string), d.Get("port_security_enabled").(bool))
	if _, _, err := client.Router.AttachInterfaceToRouter(ctx, routerID, params, nil); err != nil {
		return diag.Errorf("Error attaching network %q to router %q: %s", networkID, routerID, err)
	}

	portID, err := waitForNewInterface(ctx, client, routerID, known, d.Timeout(schema.TimeoutCreate))
	if err != nil {
		return diag.Errorf("Error waiting for the new interface on router %q: %s", routerID, err)
	}

	d.SetId(routerInterfaceID(routerID, portID))
	d.Set("port_id", portID)

	return resourceDtcloudRouterInterfaceRead(ctx, d, meta)
}

func resourceDtcloudRouterInterfaceRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	routerID := d.Get("router_id").(string)
	portID := d.Get("port_id").(string)

	interfaces, _, err := client.Router.ListRouterInterfaces(ctx, routerID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error listing the interfaces of router %q: %s", routerID, err)
	}

	for _, iface := range interfaces {
		if iface.Type != internalInterfaceType || iface.ID != portID {
			continue
		}
		d.Set("network_id", iface.NetworkID)
		d.Set("network_name", iface.Network)
		d.Set("subnet_id", iface.SubnetID)
		d.Set("ip_address", iface.IPAddress)
		d.Set("cidr", iface.Cidr)
		d.Set("status", iface.Status)
		return nil
	}

	// Detached outside Terraform, or taken down with the router.
	d.SetId("")
	return nil
}

func resourceDtcloudRouterInterfaceDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	routerID := d.Get("router_id").(string)
	portID := d.Get("port_id").(string)

	params := dtgo.DeleteRouterInterfaceParams{PortID: portID}
	if _, err := client.Router.DeleteRouterInterface(ctx, routerID, params, nil); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error detaching interface %q from router %q: %s", portID, routerID, err)
		}
	}

	err := waitForCondition(ctx, d.Timeout(schema.TimeoutDelete), func() (bool, error) {
		interfaces, _, err := client.Router.ListRouterInterfaces(ctx, routerID, nil)
		if err != nil {
			// The router went with it, which is a detached interface by any
			// other name.
			if dterr.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		return !internalPortIDs(interfaces)[portID], nil
	})
	if err != nil {
		return diag.Errorf("Error waiting for interface %q to detach from router %q: %s", portID, routerID, err)
	}

	d.SetId("")
	return nil
}

func resourceDtcloudRouterInterfaceImport(ctx context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	parts := strings.Split(d.Id(), ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("expected an id of the form <router-id>:<port-id>, got %q", d.Id())
	}
	d.Set("router_id", parts[0])
	d.Set("port_id", parts[1])
	// Nothing reports whether port security is on, so an import cannot recover
	// it. The schema default is assumed rather than left at Go's zero value,
	// which would be a plan proposing a replacement on every imported
	// interface. An interface attached with port security off has to say so in
	// the configuration; the docs say as much.
	d.Set("port_security_enabled", true)
	d.SetId(routerInterfaceID(parts[0], parts[1]))
	return []*schema.ResourceData{d}, nil
}

// waitForNewInterface returns the id of the first internal port on the router
// that was not in known. The attach endpoint reports the router rather than the
// port it created, so this is the only way to tie the new interface to a
// Terraform id.
func waitForNewInterface(ctx context.Context, client *dtgo.Client, routerID string, known map[string]bool, timeout time.Duration) (string, error) {
	var portID string
	err := waitForCondition(ctx, timeout, func() (bool, error) {
		interfaces, _, err := client.Router.ListRouterInterfaces(ctx, routerID, nil)
		if err != nil {
			return false, err
		}
		for port := range internalPortIDs(interfaces) {
			if !known[port] {
				portID = port
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return "", err
	}
	return portID, nil
}
