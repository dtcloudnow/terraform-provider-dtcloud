package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dtgo "github.com/dtcloudnow/dt-go"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudVM manages a virtual machine.
//
// What can change in place:
//   - `name`      via PUT /vms/{id}
//   - `flavor_id` via the resize action (see resizeVM: the VM is stopped first
//     unless hot plug is enabled)
//   - `state`     running / stopped / shelved, via start, softStop, hardStop,
//     shelve and unshelve
//   - `enable_hot_plug` via PUT /vms/{id}/hot-plug
//
// What is ForceNew: `network`, `block_device` and the boot-time settings
// (`key_name`, `user_data`, `script`, `is_gpu_image`). The
// API has no way to change those on a live instance. Note that interfaces and
// volumes *can* be added or removed after boot — but through their own
// endpoints, which are modelled as the separate `dtcloud_vm_network_interface`
// and `dtcloud_vm_volume_attachment` resources rather than by mutating this
// one's boot-time blocks.
//
// Read refreshes what the API reports: the details endpoint, plus the network
// interfaces and volume attachments from their own endpoints. The boot-time
// arguments above are *not* echoed back by any endpoint, so drift in them stays
// invisible to Terraform — that is an API limitation, not a choice.
//
// `vmCount` is deliberately not exposed: creating N VMs behind one resource id
// would leave Terraform tracking only the first. Use Terraform's own `count` or
// `for_each` instead.
func ResourceDtcloudVM() *schema.Resource {
	resourceSchema := map[string]*schema.Schema{
		"name": {
			Type:         schema.TypeString,
			Required:     true,
			ValidateFunc: validation.NoZeroValues,
			Description:  "Name of the virtual machine. Can be changed in place.",
		},
		"flavor_id": {
			Type:         schema.TypeString,
			Required:     true,
			ValidateFunc: validation.NoZeroValues,
			Description:  "ID of the flavor (sizing). Changing this resizes the VM in place.",
		},
		"state": {
			Type:         schema.TypeString,
			Optional:     true,
			Default:      stateRunning,
			ValidateFunc: validation.StringInSlice([]string{stateRunning, stateStopped, stateShelved}, false),
			Description:  "Desired power state: running, stopped or shelved. Shelving releases compute resources while keeping the VM and its disks.",
		},
		"graceful_shutdown": {
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     true,
			Description: "When stopping, ask the guest to shut down (softStop) instead of cutting power (hardStop).",
		},
		// key_name, user_data and script are each optional, but at least one of
		// them has to be given: they are the only ways credentials or a
		// provisioning payload reach the guest. A VM booted without any of the
		// three comes up with no way in. The web console enforces the same rule.
		"key_name": {
			Type:         schema.TypeString,
			Optional:     true,
			ForceNew:     true,
			AtLeastOneOf: []string{"key_name", "user_data", "script"},
			Description:  "Name of an existing SSH key to inject, e.g. a dtcloud_ssh_key resource's name.",
		},
		"user_data": {
			Type:         schema.TypeString,
			Optional:     true,
			ForceNew:     true,
			AtLeastOneOf: []string{"key_name", "user_data", "script"},
			Description:  "Cloud-init user data.",
		},
		"is_gpu_image": {
			Type:        schema.TypeBool,
			Optional:    true,
			ForceNew:    true,
			Description: "Set when booting a GPU image.",
		},
		"enable_hot_plug": {
			Type:        schema.TypeBool,
			Optional:    true,
			Description: "Allow live vCPU/memory resize on the instance. Can be toggled after creation.",
		},
		"network": {
			Type:        schema.TypeList,
			Required:    true,
			ForceNew:    true,
			MinItems:    1,
			Description: "Network interfaces to attach at boot.",
			Elem: &schema.Resource{
				Schema: map[string]*schema.Schema{
					"uuid": {
						Type:        schema.TypeString,
						Required:    true,
						ForceNew:    true,
						Description: "ID of the network to attach to.",
					},
					"port_security_enabled": {
						Type:        schema.TypeBool,
						Optional:    true,
						ForceNew:    true,
						Default:     true,
						Description: "Whether port security (and therefore security groups) applies to this interface.",
					},
					"security_groups": {
						Type:        schema.TypeList,
						Required:    true,
						ForceNew:    true,
						MinItems:    1,
						Elem:        &schema.Schema{Type: schema.TypeString},
						Description: "Security group IDs bound to this interface. At least one is required.",
					},
					// Required, and not merely because the API's Joi schema says
					// so. An interface built with no fixed IP comes up with no
					// address at all and the VM stays unreachable — verified live.
					// Demanding the block turns that into a plan-time error
					// instead of a VM you have to destroy and rebuild.
					"fixed_ip": {
						Type:        schema.TypeList,
						Required:    true,
						ForceNew:    true,
						MinItems:    1,
						Description: "Fixed IPs to request on this interface. At least one is required.",
						Elem: &schema.Resource{
							Schema: map[string]*schema.Schema{
								"ip_version": {
									Type:         schema.TypeInt,
									Optional:     true,
									ForceNew:     true,
									Default:      4,
									ValidateFunc: validation.IntInSlice([]int{4, 6}),
									Description:  "IP version, 4 or 6.",
								},
								"ip_address": {
									Type:        schema.TypeString,
									Optional:    true,
									ForceNew:    true,
									Description: "Specific address to request. Allocated by the platform when omitted.",
								},
							},
						},
					},
				},
			},
		},
		"block_device": {
			Type:        schema.TypeList,
			Required:    true,
			ForceNew:    true,
			MinItems:    1,
			Description: "Block devices to attach at boot. The boot disk is the entry with boot_index 0.",
			Elem: &schema.Resource{
				Schema: map[string]*schema.Schema{
					"boot_index": {
						Type:        schema.TypeInt,
						Required:    true,
						ForceNew:    true,
						Description: "Boot order. The boot disk is 0.",
					},
					"volume_size": {
						Type:         schema.TypeInt,
						Required:     true,
						ForceNew:     true,
						ValidateFunc: validation.IntBetween(1, 8192),
						Description:  "Size in GB (1-8192).",
					},
					"source_type": {
						Type:         schema.TypeString,
						Required:     true,
						ForceNew:     true,
						ValidateFunc: validation.StringInSlice([]string{"blank", "image", "volume", "backup"}, false),
						Description:  "Where the device comes from. One of: blank, image, volume, backup.",
					},
					"device_type": {
						Type:         schema.TypeString,
						Required:     true,
						ForceNew:     true,
						ValidateFunc: validation.StringInSlice([]string{"cdrom", "disk"}, false),
						Description:  "One of: cdrom, disk.",
					},
					"destination_type": {
						Type:         schema.TypeString,
						Required:     true,
						ForceNew:     true,
						ValidateFunc: validation.StringInSlice([]string{"volume", "local"}, false),
						Description:  "One of: volume, local.",
					},
					"delete_on_termination": {
						Type:        schema.TypeBool,
						Required:    true,
						ForceNew:    true,
						Description: "Whether the device is deleted with the VM.",
					},
					"uuid": {
						Type:        schema.TypeString,
						Optional:    true,
						ForceNew:    true,
						Description: "ID of the source image, volume or backup. Omitted for blank devices.",
					},
					"volume_type": {
						Type:        schema.TypeString,
						Optional:    true,
						ForceNew:    true,
						Description: "Storage policy / volume type.",
					},
				},
			},
		},
		"script": {
			Type:        schema.TypeList,
			Optional:    true,
			ForceNew:    true,
			MaxItems:    1,
			Description: "Initial credentials applied to the guest at first boot.",
			Elem: &schema.Resource{
				Schema: map[string]*schema.Schema{
					"os": {
						Type:         schema.TypeString,
						Required:     true,
						ForceNew:     true,
						ValidateFunc: validation.StringInSlice([]string{"linux", "windows"}, false),
						Description:  "Guest OS family. One of: linux, windows.",
					},
					"password": {
						Type:        schema.TypeString,
						Required:    true,
						ForceNew:    true,
						Sensitive:   true,
						Description: "Initial password for the guest account.",
					},
					"username": {
						Type:        schema.TypeString,
						Optional:    true,
						ForceNew:    true,
						Description: "Account to create.",
					},
					"hostname": {
						Type:        schema.TypeString,
						Optional:    true,
						ForceNew:    true,
						Description: "Hostname to set inside the guest.",
					},
					"disable_root": {
						Type:        schema.TypeBool,
						Optional:    true,
						ForceNew:    true,
						Description: "Disable direct root login.",
					},
				},
			},
		},
	}

	for name, s := range vmComputedSchema() {
		// name is a configurable argument on the resource, not a computed one.
		if name == "name" {
			continue
		}
		resourceSchema[name] = s
	}

	return &schema.Resource{
		CreateContext: resourceDtcloudVMCreate,
		ReadContext:   resourceDtcloudVMRead,
		UpdateContext: resourceDtcloudVMUpdate,
		DeleteContext: resourceDtcloudVMDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		Schema: resourceSchema,
		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(20 * time.Minute),
			Update: schema.DefaultTimeout(10 * time.Minute),
			Delete: schema.DefaultTimeout(15 * time.Minute),
		},
	}
}

// createVMResponse is the body returned by POST /openstack/vms. The API sends
// the OpenStack server object unwrapped, so the id sits at the top level.
// dt-go's CreateVirtualMachine hands back the raw body rather than a typed
// struct, which is why this is decoded here.
type createVMResponse struct {
	ID string `json:"id"`
}

func resourceDtcloudVMCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	opts := dtgo.CreateVirtualMachineParams{
		Name:               d.Get("name").(string),
		FlavorRef:          d.Get("flavor_id").(string),
		KeyName:            d.Get("key_name").(string),
		UserData:           d.Get("user_data").(string),
		Networks:           expandNetworks(d.Get("network").([]interface{})),
		BlockDeviceMapping: expandBlockDevices(d.Get("block_device").([]interface{})),
		Script:             expandScript(d.Get("script").([]interface{})),
	}
	if v, ok := d.GetOk("is_gpu_image"); ok {
		gpu := v.(bool)
		opts.IsGPUImage = &gpu
	}
	if v, ok := d.GetOk("enable_hot_plug"); ok {
		hotPlug := v.(bool)
		opts.EnableHotPlug = &hotPlug
	}

	body, err := client.VirtualMachine.CreateVirtualMachine(ctx, opts, nil)
	if err != nil {
		return diag.Errorf("Error creating VM: %s", err)
	}

	var created createVMResponse
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		return diag.Errorf("Error reading the created VM's id from the API response: %s (body: %s)", err, body)
	}
	if created.ID == "" {
		return diag.Errorf("The API accepted the VM but returned no id (body: %s)", body)
	}

	d.SetId(created.ID)

	// The API returns as soon as the request is accepted; the VM is still
	// building. Wait for it so that dependent resources see a usable instance.
	if _, err := waitForVMStatus(ctx, client, created.ID, []string{statusActive}, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.Errorf("Error waiting for VM %q to become ACTIVE: %s", created.ID, err)
	}

	// A VM always boots running; stop it if that is not what was asked for.
	if d.Get("state").(string) == stateStopped {
		if err := setPowerState(ctx, client, created.ID, stateStopped, d.Get("graceful_shutdown").(bool), d.Timeout(schema.TimeoutCreate)); err != nil {
			return diag.Errorf("Error stopping newly created VM %q: %s", created.ID, err)
		}
	}

	return resourceDtcloudVMRead(ctx, d, meta)
}

func resourceDtcloudVMRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	details, _, err := client.VirtualMachine.GetVirtualMachineDetails(ctx, d.Id(), nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error retrieving VM %q: %s", d.Id(), err)
	}
	if details.ID == "" {
		d.SetId("")
		return nil
	}

	setVMAttributes(d, details)
	d.Set("state", powerStateOf(details.Status))

	// The details endpoint reports the flavor by name only; resolve it so that
	// flavor_id survives an import and so an out-of-band resize shows in a plan.
	if id := resolveFlavorID(ctx, client, details.Flavor.Name); id != "" {
		d.Set("flavor_id", id)
	}

	var diags diag.Diagnostics
	for _, err := range readVMAttachments(ctx, client, d, d.Id()) {
		diags = append(diags, diag.Diagnostic{
			Severity: diag.Warning,
			Summary:  fmt.Sprintf("Could not refresh part of VM %q", d.Id()),
			Detail:   err.Error(),
		})
	}
	return diags
}

func resourceDtcloudVMUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	timeout := d.Timeout(schema.TimeoutUpdate)

	if d.HasChange("name") {
		newName := d.Get("name").(string)
		if _, err := client.VirtualMachine.UpdateVmName(ctx, d.Id(), newName, nil); err != nil {
			return diag.Errorf("Error renaming VM %q: %s", d.Id(), err)
		}
		// Renaming is asynchronous too. A stopped VM stays SHUTOFF throughout,
		// so both settled states count as done.
		if _, err := waitForVMStatus(ctx, client, d.Id(), []string{statusActive, statusShutoff}, timeout); err != nil {
			return diag.Errorf("Error waiting for VM %q after rename: %s", d.Id(), err)
		}
	}

	if d.HasChange("enable_hot_plug") {
		enabled := d.Get("enable_hot_plug").(bool)
		if _, err := client.VirtualMachine.EnableHotPlug(ctx, d.Id(), enabled, nil); err != nil {
			return diag.Errorf("Error setting hot-plug on VM %q: %s", d.Id(), err)
		}
	}

	if d.HasChange("flavor_id") {
		// Hot plug is read after the block above, so enabling it and resizing in
		// the same apply takes the online path. Without it resizeVM stops the VM
		// as the platform requires and the power block below restores the
		// configured state.
		hotPlug := d.Get("enable_hot_plug").(bool)
		if err := resizeVM(ctx, client, d.Id(), d.Get("flavor_id").(string),
			d.Get("graceful_shutdown").(bool), hotPlug, timeout); err != nil {
			return diag.Errorf("Error resizing VM %q: %s", d.Id(), err)
		}
	}

	// Power state is applied last so that a resize happens while the VM is in
	// whatever state the platform needs, and the requested state is what sticks.
	if d.HasChange("state") || d.HasChange("flavor_id") {
		desired := d.Get("state").(string)
		graceful := d.Get("graceful_shutdown").(bool)
		if err := setPowerState(ctx, client, d.Id(), desired, graceful, timeout); err != nil {
			return diag.Errorf("Error setting VM %q to %s: %s", d.Id(), desired, err)
		}
	}

	return resourceDtcloudVMRead(ctx, d, meta)
}

func resourceDtcloudVMDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	if _, err := client.VirtualMachine.DeleteVm(ctx, d.Id(), nil); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error deleting VM %q: %s", d.Id(), err)
		}
	}

	if err := waitForVMGone(ctx, client, d.Id(), d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.Errorf("Error waiting for VM %q to be deleted: %s", d.Id(), err)
	}

	d.SetId("")
	return nil
}

func expandNetworks(raw []interface{}) []dtgo.VmNetwork {
	networks := make([]dtgo.VmNetwork, 0, len(raw))
	for _, item := range raw {
		m := item.(map[string]interface{})
		// Both slices must serialise as arrays rather than null: the API
		// validates them with `Joi.array()`, which rejects null outright
		// ("'networks[0].security_groups' must be an array"). Neither field
		// carries omitempty in dt-go, so a nil slice would be sent as null.
		network := dtgo.VmNetwork{
			UUID:                m["uuid"].(string),
			PortSecurityEnabled: m["port_security_enabled"].(bool),
			SecurityGroups:      []string{},
			FixedIPs:            []dtgo.FixedIP{},
		}
		for _, g := range m["security_groups"].([]interface{}) {
			network.SecurityGroups = append(network.SecurityGroups, fmt.Sprint(g))
		}
		for _, ipItem := range m["fixed_ip"].([]interface{}) {
			ip := ipItem.(map[string]interface{})
			network.FixedIPs = append(network.FixedIPs, dtgo.FixedIP{
				IPVersion: ip["ip_version"].(int),
				IPAddress: ip["ip_address"].(string),
			})
		}
		networks = append(networks, network)
	}
	return networks
}

func expandBlockDevices(raw []interface{}) []dtgo.BlockDevice {
	devices := make([]dtgo.BlockDevice, 0, len(raw))
	for _, item := range raw {
		m := item.(map[string]interface{})
		devices = append(devices, dtgo.BlockDevice{
			BootIndex:           m["boot_index"].(int),
			UUID:                m["uuid"].(string),
			VolumeSize:          m["volume_size"].(int),
			SourceType:          m["source_type"].(string),
			DeviceType:          m["device_type"].(string),
			DestinationType:     m["destination_type"].(string),
			DeleteOnTermination: m["delete_on_termination"].(bool),
			VolumeType:          m["volume_type"].(string),
		})
	}
	return devices
}

func expandScript(raw []interface{}) *dtgo.Script {
	if len(raw) == 0 || raw[0] == nil {
		return nil
	}
	m := raw[0].(map[string]interface{})
	return &dtgo.Script{
		Os:          m["os"].(string),
		Password:    m["password"].(string),
		Username:    m["username"].(string),
		Hostname:    m["hostname"].(string),
		DisableRoot: m["disable_root"].(bool),
	}
}

func expandStringList(raw []interface{}) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, fmt.Sprint(item))
	}
	return out
}
