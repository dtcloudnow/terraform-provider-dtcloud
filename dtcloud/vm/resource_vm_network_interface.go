package vm

import (
	"context"
	"fmt"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudVMNetworkInterface attaches an extra network interface to a VM.
//
// Interfaces declared in a dtcloud_vm's `network` blocks are created with the
// VM and live and die with it. This resource is for the ones added afterwards:
// they have their own lifecycle, get their own port id, and can be detached
// without recreating the VM.
//
// The resource id is `<vm-id>:<port-id>`. The port id is assigned by the
// platform at attach time, so it is discovered by diffing the VM's interface
// list before and after the call — the attach endpoint does not return it.
func ResourceDtcloudVMNetworkInterface() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceDtcloudVMNetworkInterfaceCreate,
		ReadContext:   resourceDtcloudVMNetworkInterfaceRead,
		UpdateContext: resourceDtcloudVMNetworkInterfaceUpdate,
		DeleteContext: resourceDtcloudVMNetworkInterfaceDelete,
		Importer: &schema.ResourceImporter{
			StateContext: resourceDtcloudVMNetworkInterfaceImport,
		},

		Schema: map[string]*schema.Schema{
			"vm_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the virtual machine to attach the interface to.",
			},
			"network_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the network to attach to.",
			},
			// Read-only. The platform assigns the MAC and reports it back;
			// requesting a specific one is not offered.
			"mac_address": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "MAC address the platform assigned to this interface.",
			},
			"security_groups": {
				Type:        schema.TypeList,
				Optional:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "Security group IDs bound to this interface. Can be changed in place.",
			},
			// Required: an interface with no fixed IP comes up with no address.
			"fixed_ip": {
				Type:        schema.TypeList,
				Required:    true,
				MinItems:    1,
				Description: "Fixed IPs on this interface. At least one is required. Can be changed in place.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"ip_version": {
							Type:         schema.TypeInt,
							Optional:     true,
							Default:      4,
							ValidateFunc: validation.IntInSlice([]int{4, 6}),
							Description:  "IP version, 4 or 6.",
						},
						"ip_address": {
							Type:        schema.TypeString,
							Optional:    true,
							Computed:    true,
							Description: "Specific address to request. Allocated by the platform when omitted.",
						},
					},
				},
			},
			"port_security_enabled": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     true,
				ForceNew:    true,
				Description: "Whether port security (and therefore security groups) applies to this interface.",
			},

			"port_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "ID of the port the platform created for this interface.",
			},
			"network_name": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Name of the network.",
			},
			"primary_ip": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Primary IP address on this interface.",
			},
			"is_public": {
				Type:        schema.TypeBool,
				Computed:    true,
				Description: "Whether the interface is on a public network.",
			},
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(10 * time.Minute),
			Update: schema.DefaultTimeout(10 * time.Minute),
			Delete: schema.DefaultTimeout(10 * time.Minute),
		},
	}
}

func networkInterfaceID(vmID, portID string) string {
	return fmt.Sprintf("%s:%s", vmID, portID)
}

func resourceDtcloudVMNetworkInterfaceCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	vmID := d.Get("vm_id").(string)
	networkID := d.Get("network_id").(string)

	// Record the ports that already exist so the new one can be identified.
	before, _, err := client.VirtualMachine.GetVmNetworkInterfaces(ctx, vmID, nil)
	if err != nil {
		return diag.Errorf("Error listing interfaces on VM %q: %s", vmID, err)
	}
	known := make(map[string]bool, len(before))
	for _, iface := range before {
		known[iface.PortID] = true
	}

	params := dtgo.AttachNetworkInterfaceToVmParams{
		NetworkId:           networkID,
		SecurityGroups:      expandStringList(d.Get("security_groups").([]interface{})),
		PortSecurityEnabled: d.Get("port_security_enabled").(bool),
		FixedIps:            expandFixedIPs(d.Get("fixed_ip").([]interface{})),
	}

	if _, err := client.VirtualMachine.AttachNetworkInterfaceToVm(ctx, vmID, params, nil); err != nil {
		return diag.Errorf("Error attaching an interface on network %q to VM %q: %s", networkID, vmID, err)
	}

	portID, err := waitForNewPort(ctx, client, vmID, known, d.Timeout(schema.TimeoutCreate))
	if err != nil {
		return diag.Errorf("Error waiting for the new interface on VM %q: %s", vmID, err)
	}

	d.SetId(networkInterfaceID(vmID, portID))
	d.Set("port_id", portID)

	return resourceDtcloudVMNetworkInterfaceRead(ctx, d, meta)
}

func resourceDtcloudVMNetworkInterfaceRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	vmID := d.Get("vm_id").(string)
	portID := d.Get("port_id").(string)

	interfaces, _, err := client.VirtualMachine.GetVmNetworkInterfaces(ctx, vmID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error listing interfaces on VM %q: %s", vmID, err)
	}

	for _, iface := range interfaces {
		if iface.PortID != portID {
			continue
		}
		d.Set("network_id", iface.ID)
		d.Set("network_name", iface.NetworkName)
		d.Set("mac_address", iface.MacAddress)
		d.Set("primary_ip", iface.PrimaryIP)
		d.Set("is_public", iface.IsPublic)
		d.Set("port_security_enabled", iface.SpoofingProtection)

		groups := make([]string, 0, len(iface.SecurityGroups))
		for _, g := range iface.SecurityGroups {
			groups = append(groups, g.ID)
		}
		d.Set("security_groups", groups)
		return nil
	}

	// Detached outside Terraform.
	d.SetId("")
	return nil
}

func resourceDtcloudVMNetworkInterfaceUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	vmID := d.Get("vm_id").(string)
	portID := d.Get("port_id").(string)

	params := dtgo.UpdateNetworkInterfaceParams{
		PortSecurityEnabled: d.Get("port_security_enabled").(bool),
		SecurityGroups:      expandStringList(d.Get("security_groups").([]interface{})),
		FixedIPs:            expandFixedIPs(d.Get("fixed_ip").([]interface{})),
	}

	if _, err := client.VirtualMachine.UpdateNetworkInterface(ctx, vmID, portID, params, nil); err != nil {
		return diag.Errorf("Error updating interface %q on VM %q: %s", portID, vmID, err)
	}

	return resourceDtcloudVMNetworkInterfaceRead(ctx, d, meta)
}

func resourceDtcloudVMNetworkInterfaceDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	vmID := d.Get("vm_id").(string)
	portID := d.Get("port_id").(string)

	if _, err := client.VirtualMachine.DetachNetworkFromVm(ctx, vmID, portID, nil); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error detaching interface %q from VM %q: %s", portID, vmID, err)
		}
	}

	err := waitForCondition(ctx, d.Timeout(schema.TimeoutDelete), func() (bool, error) {
		interfaces, _, err := client.VirtualMachine.GetVmNetworkInterfaces(ctx, vmID, nil)
		if err != nil {
			if dterr.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		for _, iface := range interfaces {
			if iface.PortID == portID {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return diag.Errorf("Error waiting for interface %q to detach from VM %q: %s", portID, vmID, err)
	}

	d.SetId("")
	return nil
}

func resourceDtcloudVMNetworkInterfaceImport(ctx context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	parts := strings.Split(d.Id(), ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("expected an id of the form <vm-id>:<port-id>, got %q", d.Id())
	}
	d.Set("vm_id", parts[0])
	d.Set("port_id", parts[1])
	d.SetId(networkInterfaceID(parts[0], parts[1]))
	return []*schema.ResourceData{d}, nil
}

// waitForNewPort returns the id of the first port on the VM that was not in
// known. The attach endpoint does not report which port it created, so this is
// the only way to tie the new interface to a Terraform id.
func waitForNewPort(ctx context.Context, client *dtgo.Client, vmID string, known map[string]bool, timeout time.Duration) (string, error) {
	var portID string
	err := waitForCondition(ctx, timeout, func() (bool, error) {
		interfaces, _, err := client.VirtualMachine.GetVmNetworkInterfaces(ctx, vmID, nil)
		if err != nil {
			return false, err
		}
		for _, iface := range interfaces {
			if iface.PortID != "" && !known[iface.PortID] {
				portID = iface.PortID
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

func expandFixedIPs(raw []interface{}) []dtgo.FixedIP {
	ips := make([]dtgo.FixedIP, 0, len(raw))
	for _, item := range raw {
		m := item.(map[string]interface{})
		ips = append(ips, dtgo.FixedIP{
			IPVersion: m["ip_version"].(int),
			IPAddress: m["ip_address"].(string),
		})
	}
	return ips
}
