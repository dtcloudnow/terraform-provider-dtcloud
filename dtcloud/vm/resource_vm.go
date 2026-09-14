package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/go-cty/cty"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudVM manages a virtual machine. Adding or removing a `network`
// or `block_device` block replaces the instance; dtcloud_vm_network_interface
// and dtcloud_vm_volume_attachment attach more after boot.
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
			Description: "ID of the flavor (sizing). Changing this resizes the instance in place. " +
				"Unless enable_hot_plug is true the platform requires the instance to be stopped " +
				"first, so the provider stops it, resizes and returns it to the configured power " +
				"state — a plan that does this is the one where `status` also shows as (known " +
				"after apply).",
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
		// key_name, user_data or script: the only ways credentials or a provisioning
		// payload reach the guest.
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
			Type:     schema.TypeBool,
			Optional: true,
			Computed: true,
			Description: "Allow live vCPU/memory resize on the instance. With this on, changing " +
				"flavor_id does not power-cycle the instance. Can be toggled after creation.",
		},
		"metadata": {
			Type:        schema.TypeMap,
			Computed:    true,
			Elem:        &schema.Schema{Type: schema.TypeString},
			Description: "Metadata the platform attaches to the instance, e.g. ha_enabled.",
		},
		// Optional+Computed so an import fills the blocks in. The "at least one"
		// rule is in CustomizeDiff, which can tell a create from a refresh.
		"network": {
			Type:        schema.TypeList,
			Optional:    true,
			Computed:    true,
			ForceNew:    true,
			Description: "Network interfaces to attach at boot. At least one is required.",
			Elem: &schema.Resource{
				Schema: map[string]*schema.Schema{
					"uuid": {
						Type:        schema.TypeString,
						Required:    true,
						ForceNew:    true,
						Description: "ID of the network to attach to.",
					},
					// No Default: left unset, the network decides.
					"port_security_enabled": {
						Type:        schema.TypeBool,
						Optional:    true,
						Computed:    true,
						ForceNew:    true,
						Description: "Whether port security (and therefore security groups) applies to this interface. Defaults to the network's own setting.",
					},
					// Not ForceNew: groups are rebound on the existing port.
					"security_groups": {
						Type:        schema.TypeList,
						Optional:    true,
						Computed:    true,
						Elem:        &schema.Schema{Type: schema.TypeString},
						Description: "Security group IDs bound to this interface. Can be changed in place. Defaults to the project's default group.",
					},
					// An empty fixed_ips list means "no address", not "allocate one".
					"fixed_ip": {
						Type:        schema.TypeList,
						Optional:    true,
						Computed:    true,
						Description: "Fixed IPs on this interface. Omit to have one address allocated. Can be changed in place.",
						Elem: &schema.Resource{
							Schema: map[string]*schema.Schema{
								// Advisory, and omitted when a pinned address implies the version.
								"ip_version": {
									Type:         schema.TypeInt,
									Optional:     true,
									Computed:     true,
									ValidateFunc: validation.IntInSlice([]int{4, 6}),
									Description:  "IP version, 4 or 6. Advisory on write — the platform allocates from the attached network; pin ip_address to choose an address. Reported back from the assigned address.",
								},
								"ip_address": {
									Type:         schema.TypeString,
									Optional:     true,
									Computed:     true,
									ValidateFunc: validation.IsIPAddress,
									Description:  "Specific address to request. Allocated by the platform when omitted, and reported back here either way.",
								},
							},
						},
					},
				},
			},
		},
		// Optional+Computed so an import fills the blocks in from the volumes.
		"block_device": {
			Type:        schema.TypeList,
			Optional:    true,
			Computed:    true,
			ForceNew:    true,
			Description: "Block devices to attach at boot. The boot disk is the entry with boot_index 0. At least one is required.",
			Elem: &schema.Resource{
				Schema: map[string]*schema.Schema{
					"boot_index": {
						Type:        schema.TypeInt,
						Required:    true,
						ForceNew:    true,
						Description: "Boot order. The boot disk is 0.",
					},
					// Not ForceNew: the volume grows in place. There is no shrink, so a
					// decrease is refused in CustomizeDiff.
					"volume_size": {
						Type:         schema.TypeInt,
						Required:     true,
						ValidateFunc: validation.IntBetween(1, 8192),
						Description:  "Size in GB (1-8192). Can be increased in place; decreasing is not supported.",
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
					// Not ForceNew either: the storage policy changes in place.
					"volume_type": {
						Type:         schema.TypeString,
						Required:     true,
						ValidateFunc: validation.NoZeroValues,
						Description:  "Storage policy / volume type the disk is created on. Can be changed in place.",
					},
				},
			},
		},
		"script": {
			Type:     schema.TypeList,
			Optional: true,
			ForceNew: true,
			MaxItems: 1,
			Description: "Initial credentials applied to the guest at first boot. On a Linux " +
				"image, set username, hostname and disable_root as well — they are optional only " +
				"because Windows template images ignore them.",
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
						Description: "Account to create. Required in practice on Linux images; ignored by Windows template images.",
					},
					"hostname": {
						Type:        schema.TypeString,
						Optional:    true,
						ForceNew:    true,
						Description: "Hostname to set inside the guest. Required in practice on Linux images; ignored by Windows template images.",
					},
					"disable_root": {
						Type:        schema.TypeBool,
						Optional:    true,
						ForceNew:    true,
						Description: "Disable direct root login. Required in practice on Linux images; ignored by Windows template images.",
					},
				},
			},
		},
	}

	for name, s := range vmComputedSchema() {
		// name is a configurable argument, not a computed one.
		if name == "name" {
			continue
		}
		resourceSchema[name] = s
	}

	return &schema.Resource{
		Description: "Manages a virtual machine.",

		CreateContext: resourceDtcloudVMCreate,
		ReadContext:   resourceDtcloudVMRead,
		UpdateContext: resourceDtcloudVMUpdate,
		DeleteContext: resourceDtcloudVMDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		Schema: resourceSchema,
		// user_data wins: setting both discards the script.
		CustomizeDiff: func(ctx context.Context, d *schema.ResourceDiff, meta interface{}) error {
			// Create only: on a refresh an empty list means the ports are unread.
			if d.Id() == "" && len(d.Get("network").([]interface{})) == 0 {
				return fmt.Errorf("at least one network block is required: an instance with no interface has no address and cannot be reached")
			}
			if d.Id() == "" && len(d.Get("block_device").([]interface{})) == 0 {
				return fmt.Errorf("at least one block_device is required: an instance needs a disk to boot from")
			}
			// Read from the raw configuration: d.Get cannot tell "unset" from "false".
			if err := checkPortSecurityConflict(d.GetRawConfig()); err != nil {
				return err
			}
			// A volume can be grown but never shrunk. Refused rather than turned into a
			// replacement, which would destroy the data on the disk.
			if d.Id() != "" && d.HasChange("block_device") {
				oldRaw, newRaw := d.GetChange("block_device")
				oldDevices, _ := oldRaw.([]interface{})
				newDevices, _ := newRaw.([]interface{})
				for i, raw := range newDevices {
					if i >= len(oldDevices) {
						break
					}
					prev, ok1 := oldDevices[i].(map[string]interface{})
					next, ok2 := raw.(map[string]interface{})
					if !ok1 || !ok2 {
						continue
					}
					before, _ := prev["volume_size"].(int)
					after, _ := next["volume_size"].(int)
					if after < before {
						return fmt.Errorf("block_device[%d]: volume_size cannot be decreased (%d -> %d): "+
							"the platform can grow a volume but not shrink one", i, before, after)
					}
				}
			}
			// Unless these are marked unknown the plan keeps the old values and
			// anything referencing them lags a cycle behind.
			if d.Id() != "" && d.HasChange("network") {
				for _, key := range []string{"primary_ip", "network_interface"} {
					if err := d.SetNewComputed(key); err != nil {
						return err
					}
				}
			}
			// Computed attributes are planned as unchanged unless told otherwise.
			if d.Id() != "" && d.HasChange("flavor_id") {
				for _, key := range []string{"vcpus", "ram", "flavor_name"} {
					if err := d.SetNewComputed(key); err != nil {
						return err
					}
				}
				// Without hot plug the instance passes through SHUTOFF, and an unknown
				// status is the only signal a plan can carry.
				if !d.Get("enable_hot_plug").(bool) {
					if err := d.SetNewComputed("status"); err != nil {
						return err
					}
				}
			}
			// The same for the volume snapshot when a disk is grown or retyped.
			if d.Id() != "" && d.HasChange("block_device") {
				if err := d.SetNewComputed("volume"); err != nil {
					return err
				}
			}
			hasScript := len(d.Get("script").([]interface{})) > 0
			if d.Get("user_data").(string) != "" && hasScript {
				return fmt.Errorf("user_data and script cannot both be set: the platform builds " +
					"cloud-init from script only when user_data is absent, so setting both would " +
					"silently discard the script block")
			}
			return nil
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(20 * time.Minute),
			Update: schema.DefaultTimeout(10 * time.Minute),
			Delete: schema.DefaultTimeout(15 * time.Minute),
		},
	}
}

// createVMResponse is the create body: the server object comes back unwrapped,
// so the id sits at the top level.
type createVMResponse struct {
	ID string `json:"id"`
}

// warnIncompleteLinuxScript flags a `script` a Linux guest will not be fully
// configured by. The rule depends on `os`, which the schema cannot express.
func warnIncompleteLinuxScript(d *schema.ResourceData) diag.Diagnostics {
	raw := d.Get("script").([]interface{})
	if len(raw) == 0 || raw[0] == nil {
		return nil
	}
	script := raw[0].(map[string]interface{})
	if script["os"].(string) != "linux" {
		return nil
	}

	missing := []string{}
	if script["username"].(string) == "" {
		missing = append(missing, "username")
	}
	if script["hostname"].(string) == "" {
		missing = append(missing, "hostname")
	}
	if _, set := d.GetOkExists("script.0.disable_root"); !set {
		missing = append(missing, "disable_root")
	}
	if len(missing) == 0 {
		return nil
	}

	return diag.Diagnostics{{
		Severity: diag.Warning,
		Summary:  "Incomplete script block for a Linux image",
		Detail: "script.os is \"linux\" but " + strings.Join(missing, ", ") +
			" not set. These are optional only because Windows template images ignore " +
			"them; on Linux the guest is configured with the platform's defaults instead " +
			"of yours. Set them, or drop the script block if you are provisioning another way.",
	}}
}

func resourceDtcloudVMCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	warnings := warnIncompleteLinuxScript(d)

	opts := dtgo.CreateVirtualMachineParams{
		Name:               d.Get("name").(string),
		FlavorRef:          d.Get("flavor_id").(string),
		KeyName:            d.Get("key_name").(string),
		UserData:           d.Get("user_data").(string),
		Networks:           expandNetworks(d.Get("network").([]interface{}), portSecurityOverrides(d)),
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

	// The request returns before the VM is built; wait so dependants see it usable.
	if _, err := waitForVMStatus(ctx, client, created.ID, []string{statusActive}, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.Errorf("Error waiting for VM %q to become ACTIVE: %s", created.ID, err)
	}

	// A VM always boots running; stop it if that is not what was asked for.
	if d.Get("state").(string) == stateStopped {
		if err := setPowerState(ctx, client, created.ID, stateStopped, d.Get("graceful_shutdown").(bool), d.Timeout(schema.TimeoutCreate)); err != nil {
			return diag.Errorf("Error stopping newly created VM %q: %s", created.ID, err)
		}
	}

	return append(warnings, resourceDtcloudVMRead(ctx, d, meta)...)
}

func resourceDtcloudVMRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	details, body, err := client.VirtualMachine.GetVirtualMachineDetails(ctx, d.Id(), nil)
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
	setVMExtras(d, body)
	d.Set("state", powerStateOf(details.Status))

	// Set here so it survives an import and an out-of-band swap shows in a plan.
	d.Set("key_name", details.SSHKey)

	// Only the flavor name is reported; resolve it so flavor_id survives an import.
	if id := resolveFlavorID(ctx, client, details.Flavor.Name); id != "" {
		d.Set("flavor_id", id)
	}

	var diags diag.Diagnostics
	for _, err := range readVMAttachments(ctx, client, d, d.Id(), details.ImageID) {
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
	warnings := disruptiveChangeWarnings(d)

	if d.HasChange("name") {
		newName := d.Get("name").(string)
		if _, err := client.VirtualMachine.UpdateVmName(ctx, d.Id(), newName, nil); err != nil {
			return diag.Errorf("Error renaming VM %q: %s", d.Id(), err)
		}
		// A stopped VM stays SHUTOFF throughout, so both settled states count as done.
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
		// After the hot-plug block, so enabling it and resizing in one apply stays online.
		hotPlug := d.Get("enable_hot_plug").(bool)
		if err := resizeVM(ctx, client, d.Id(), d.Get("flavor_id").(string),
			d.Get("graceful_shutdown").(bool), hotPlug, timeout); err != nil {
			return diag.Errorf("Error resizing VM %q: %s", d.Id(), err)
		}
	}

	if d.HasChange("block_device") {
		if err := applyBlockDeviceChanges(ctx, client, d, timeout); err != nil {
			return diag.Errorf("Error updating disks on VM %q: %s", d.Id(), err)
		}
	}

	// Before the power block, so the ports are still reachable while they are edited.
	if d.HasChange("network") {
		if err := applyNetworkChanges(ctx, client, d, timeout); err != nil {
			return diag.Errorf("Error updating interfaces on VM %q: %s", d.Id(), err)
		}
	}

	// Last, so the requested state is what sticks.
	if d.HasChange("state") || d.HasChange("flavor_id") {
		desired := d.Get("state").(string)
		graceful := d.Get("graceful_shutdown").(bool)
		if err := setPowerState(ctx, client, d.Id(), desired, graceful, timeout); err != nil {
			return diag.Errorf("Error setting VM %q to %s: %s", d.Id(), desired, err)
		}
	}

	return append(warnings, resourceDtcloudVMRead(ctx, d, meta)...)
}

// disruptiveChangeWarnings reports in-place updates that do more to the running
// machine than the plan shows. Raised from Update and read in the past tense.
func disruptiveChangeWarnings(d *schema.ResourceData) diag.Diagnostics {
	var warnings diag.Diagnostics

	// The new value: Update turns hot plug on before resizing.
	if d.HasChange("flavor_id") && !d.Get("enable_hot_plug").(bool) {
		warnings = append(warnings, diag.Diagnostic{
			Severity: diag.Warning,
			Summary:  "The instance was stopped and restarted to resize it",
			Detail: "The platform refuses to resize a running instance, so the provider stopped it, " +
				"applied the new flavor and returned it to the configured power state. Anything " +
				"running inside was interrupted. Setting enable_hot_plug = true resizes vCPU and " +
				"memory on a running instance instead — a plan that power-cycles the machine is " +
				"the one where `status` also shows as (known after apply).",
		})
	}

	if grown := grownDevices(d); len(grown) > 0 {
		warnings = append(warnings, diag.Diagnostic{
			Severity: diag.Warning,
			Summary:  fmt.Sprintf("The disk behind %s is bigger, the filesystem on it is not", strings.Join(grown, ", ")),
			Detail: "Growing a volume enlarges the block device only. The partition table and the " +
				"filesystem inside the guest still describe the old size, and nothing outside the " +
				"guest can change that. Extend them from inside the instance — growpart and " +
				"resize2fs, or the equivalent for your filesystem — or the extra space stays unused.",
		})
	}

	if repinned := repinnedAddresses(d); len(repinned) > 0 {
		warnings = append(warnings, diag.Diagnostic{
			Severity: diag.Warning,
			Summary:  fmt.Sprintf("The guest is not aware of its new address (%s)", strings.Join(repinned, ", ")),
			Detail: "The address was changed on the port, which is where the platform enforces it. " +
				"The operating system inside still holds the old one until its DHCP lease is " +
				"renewed, and forever if the interface is configured statically — so the instance " +
				"may be unreachable in the meantime. Renew the lease or reboot the instance.",
		})
	}

	return warnings
}

// grownDevices names the block_device entries whose volume is being enlarged.
func grownDevices(d *schema.ResourceData) []string {
	if !d.HasChange("block_device") {
		return nil
	}
	oldRaw, newRaw := d.GetChange("block_device")
	oldDevices, _ := oldRaw.([]interface{})
	newDevices, _ := newRaw.([]interface{})

	var grown []string
	for i, raw := range newDevices {
		if i >= len(oldDevices) {
			break
		}
		prev, ok1 := oldDevices[i].(map[string]interface{})
		next, ok2 := raw.(map[string]interface{})
		if !ok1 || !ok2 {
			continue
		}
		before, _ := prev["volume_size"].(int)
		after, _ := next["volume_size"].(int)
		if after > before {
			grown = append(grown, fmt.Sprintf("block_device[%d]", i))
		}
	}
	return grown
}

// repinnedAddresses lists addresses an existing interface is moving to. One the
// platform allocated on its own is not among them.
func repinnedAddresses(d *schema.ResourceData) []string {
	if !d.HasChange("network") {
		return nil
	}
	oldRaw, newRaw := d.GetChange("network")
	oldBlocks, _ := oldRaw.([]interface{})
	newBlocks, _ := newRaw.([]interface{})

	var moved []string
	for i, raw := range newBlocks {
		if i >= len(oldBlocks) {
			break
		}
		prev, ok1 := oldBlocks[i].(map[string]interface{})
		next, ok2 := raw.(map[string]interface{})
		if !ok1 || !ok2 {
			continue
		}
		before := addressSet(prev["fixed_ip"])
		for _, address := range addressList(next["fixed_ip"]) {
			if address != "" && !before[address] {
				moved = append(moved, address)
			}
		}
	}
	return moved
}

func addressList(raw interface{}) []string {
	entries, _ := raw.([]interface{})
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		m, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		if address, _ := m["ip_address"].(string); address != "" {
			out = append(out, address)
		}
	}
	return out
}

func addressSet(raw interface{}) map[string]bool {
	set := map[string]bool{}
	for _, address := range addressList(raw) {
		set[address] = true
	}
	return set
}

// applyBlockDeviceChanges grows and retypes the boot-time volumes, so a bigger
// disk does not mean rebuilding. Only size and type are actionable.
func applyBlockDeviceChanges(ctx context.Context, client *dtgo.Client, d *schema.ResourceData, timeout time.Duration) error {
	volumes, _, err := client.VirtualMachine.GetVmVolumeAttachments(ctx, d.Id(), nil)
	if err != nil {
		return fmt.Errorf("listing volumes: %w", err)
	}

	oldRaw, newRaw := d.GetChange("block_device")
	oldDevices, _ := oldRaw.([]interface{})
	newDevices, _ := newRaw.([]interface{})
	// Matched on the old devices: they carry the size and policy the volumes have.
	matched := matchVolumesToBlockDevices(oldDevices, volumes)

	for i, raw := range newDevices {
		next, ok := raw.(map[string]interface{})
		if !ok || i >= len(oldDevices) {
			continue
		}
		prev, ok := oldDevices[i].(map[string]interface{})
		if !ok {
			continue
		}
		if i >= len(matched) || matched[i] < 0 {
			return fmt.Errorf("block_device[%d] cannot be matched to a volume on the instance: "+
				"beyond the boot disk the platform reports no ordering, so a device is only "+
				"claimed when exactly one attached volume has its recorded size and volume_type. "+
				"Manage this disk through dtcloud_vm_volume_attachment instead", i)
		}
		volumeID := volumes[matched[i]].ID

		if before, after := prev["volume_size"].(int), next["volume_size"].(int); after > before {
			params := dtgo.PerformActionParams{OsExtend: &dtgo.OsExtend{NewSize: after}}
			if _, err := client.Volume.PerformActionOnVolume(ctx, volumeID, params, nil); err != nil {
				return fmt.Errorf("growing volume %q to %d GB: %w", volumeID, after, err)
			}
			if err := waitForVolume(ctx, client, d.Id(), volumeID, timeout, func(v vmVolume) bool {
				return v.size >= after
			}); err != nil {
				return fmt.Errorf("waiting for volume %q to reach %d GB: %w", volumeID, after, err)
			}
		}

		if before, after := prev["volume_type"].(string), next["volume_type"].(string); after != before && after != "" {
			params := dtgo.PerformActionParams{OsRetype: &dtgo.OsRetype{NewType: after}}
			if _, err := client.Volume.PerformActionOnVolume(ctx, volumeID, params, nil); err != nil {
				return fmt.Errorf("retyping volume %q to %q: %w", volumeID, after, err)
			}
			if err := waitForVolume(ctx, client, d.Id(), volumeID, timeout, func(v vmVolume) bool {
				return v.storagePolicy == after
			}); err != nil {
				return fmt.Errorf("waiting for volume %q to become %q: %w", volumeID, after, err)
			}
		}
	}
	return nil
}

// vmVolume is the slice of a volume attachment these waiters compare against.
type vmVolume struct {
	size          int
	storagePolicy string
}

// waitForVolume blocks until the VM's copy of the volume satisfies done. Both
// actions return before the change is visible.
func waitForVolume(ctx context.Context, client *dtgo.Client, vmID, volumeID string,
	timeout time.Duration, done func(vmVolume) bool) error {

	return waitForCondition(ctx, timeout, func() (bool, error) {
		volumes, _, err := client.VirtualMachine.GetVmVolumeAttachments(ctx, vmID, nil)
		if err != nil {
			return false, err
		}
		for _, v := range volumes {
			if v.ID == volumeID {
				return done(vmVolume{size: v.Size, storagePolicy: v.StoragePolicy}), nil
			}
		}
		// Gone from the instance; nothing further to wait for.
		return true, nil
	})
}

// applyNetworkChanges rebinds security groups and fixed IPs on the existing
// ports. Matching uses the old blocks, which the ports were created from.
func applyNetworkChanges(ctx context.Context, client *dtgo.Client, d *schema.ResourceData, timeout time.Duration) error {
	interfaces, _, err := client.VirtualMachine.GetVmNetworkInterfaces(ctx, d.Id(), nil)
	if err != nil {
		return fmt.Errorf("listing interfaces: %w", err)
	}

	oldRaw, newRaw := d.GetChange("network")
	oldBlocks, _ := oldRaw.([]interface{})
	newBlocks, _ := newRaw.([]interface{})
	matched := matchPortsToNetworkBlocks(oldBlocks, interfaces)

	for i, raw := range newBlocks {
		if i >= len(matched) || matched[i] < 0 {
			// A block with no port behind it is a new interface, and adding one
			// replaces the VM, so this never has to create anything.
			continue
		}
		block, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if i < len(oldBlocks) && networkBlockUnchanged(oldBlocks[i], block) {
			continue
		}

		fixedIPs := expandFixedIPs(block["fixed_ip"].([]interface{}))
		if len(fixedIPs) == 0 {
			// An empty list or entry is refused, so the port's own addresses stand in.
			fixedIPs = expandFixedIPs(fixedIPBlocksOf(portAddresses(interfaces, matched[i])))
		}

		params := dtgo.UpdateNetworkInterfaceParams{
			SecurityGroups: expandStringList(block["security_groups"].([]interface{})),
			FixedIPs:       fixedIPs,
		}
		portID := interfaces[matched[i]].PortID
		if _, err := client.VirtualMachine.UpdateNetworkInterface(ctx, d.Id(), portID, params, nil); err != nil {
			return fmt.Errorf("updating port %q: %w", portID, err)
		}
		// The call returns before the port reflects the change.
		if err := waitForPortSettled(ctx, client, d.Id(), portID, params, timeout); err != nil {
			return fmt.Errorf("waiting for port %q to settle: %w", portID, err)
		}
	}
	return nil
}

// waitForPortSettled blocks until the port reports the groups and addresses the
// update asked for. Only pinned values are waited on.
func waitForPortSettled(ctx context.Context, client *dtgo.Client, vmID, portID string,
	want dtgo.UpdateNetworkInterfaceParams, timeout time.Duration) error {

	wantGroups := make(map[string]bool, len(want.SecurityGroups))
	for _, g := range want.SecurityGroups {
		wantGroups[g] = true
	}
	var wantAddresses []string
	for _, ip := range want.FixedIPs {
		if ip.IPAddress != "" {
			wantAddresses = append(wantAddresses, ip.IPAddress)
		}
	}

	return waitForCondition(ctx, timeout, func() (bool, error) {
		interfaces, _, err := client.VirtualMachine.GetVmNetworkInterfaces(ctx, vmID, nil)
		if err != nil {
			return false, err
		}
		for i := range interfaces {
			if interfaces[i].PortID != portID {
				continue
			}
			if len(wantGroups) > 0 {
				if len(interfaces[i].SecurityGroups) != len(wantGroups) {
					return false, nil
				}
				for _, g := range interfaces[i].SecurityGroups {
					if !wantGroups[g.ID] {
						return false, nil
					}
				}
			}
			have := make(map[string]bool)
			for _, ip := range portAddresses(interfaces, i) {
				have[ip] = true
			}
			for _, ip := range wantAddresses {
				if !have[ip] {
					return false, nil
				}
			}
			return true, nil
		}
		// The port is gone; nothing left to wait for.
		return true, nil
	})
}

// networkBlockUnchanged reports whether the two actionable fields match, so an
// edit elsewhere in the list does not send a call for every interface.
func networkBlockUnchanged(oldRaw interface{}, next map[string]interface{}) bool {
	prev, ok := oldRaw.(map[string]interface{})
	if !ok {
		return false
	}
	return reflect.DeepEqual(prev["security_groups"], next["security_groups"]) &&
		reflect.DeepEqual(prev["fixed_ip"], next["fixed_ip"])
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

// portSecurityOverrides reports, per network block, whether the configuration
// set port_security_enabled. A nil entry keeps the field out of the body.
func portSecurityOverrides(d *schema.ResourceData) []*bool {
	raw := d.GetRawConfig()
	if raw.IsNull() || !raw.IsKnown() {
		return nil
	}
	networks := raw.GetAttr("network")
	if networks.IsNull() || !networks.IsKnown() {
		return nil
	}

	out := make([]*bool, 0, networks.LengthInt())
	for it := networks.ElementIterator(); it.Next(); {
		_, block := it.Element()
		if block.IsNull() || !block.IsKnown() {
			out = append(out, nil)
			continue
		}
		v := block.GetAttr("port_security_enabled")
		if v.IsNull() || !v.IsKnown() {
			out = append(out, nil)
			continue
		}
		enabled := v.True()
		out = append(out, &enabled)
	}
	return out
}

// checkPortSecurityConflict rejects a network block that disables port security
// while naming groups. Only the raw configuration can tell unset from false.
func checkPortSecurityConflict(raw cty.Value) error {
	if raw.IsNull() || !raw.IsKnown() {
		return nil
	}
	networks := raw.GetAttr("network")
	if networks.IsNull() || !networks.IsKnown() {
		return nil
	}

	i := -1
	for it := networks.ElementIterator(); it.Next(); {
		i++
		_, block := it.Element()
		if block.IsNull() || !block.IsKnown() {
			continue
		}
		enabled := block.GetAttr("port_security_enabled")
		if enabled.IsNull() || !enabled.IsKnown() || enabled.True() {
			continue
		}
		groups := block.GetAttr("security_groups")
		if groups.IsNull() || !groups.IsKnown() || groups.LengthInt() == 0 {
			continue
		}
		return fmt.Errorf("network[%d]: security_groups cannot be set while port_security_enabled is false: "+
			"a port with port security disabled cannot carry security groups", i)
	}
	return nil
}

func expandNetworks(raw []interface{}, portSecurity []*bool) []dtgo.VmNetwork {
	networks := make([]dtgo.VmNetwork, 0, len(raw))
	for i, item := range raw {
		m := item.(map[string]interface{})
		// Both must serialise as arrays rather than null, and neither carries omitempty.
		network := dtgo.VmNetwork{
			UUID:           m["uuid"].(string),
			SecurityGroups: []string{},
			FixedIPs:       []dtgo.FixedIP{},
		}
		if i < len(portSecurity) {
			network.PortSecurityEnabled = portSecurity[i]
		}
		for _, g := range m["security_groups"].([]interface{}) {
			network.SecurityGroups = append(network.SecurityGroups, fmt.Sprint(g))
		}
		for _, ipItem := range m["fixed_ip"].([]interface{}) {
			ip, ok := ipItem.(map[string]interface{})
			if !ok {
				continue
			}
			address, _ := ip["ip_address"].(string)
			if address != "" {
				// A pinned address implies its version, so nothing else is sent.
				network.FixedIPs = append(network.FixedIPs, dtgo.FixedIP{IPAddress: address})
				continue
			}
			// `{ip_version: 4}` is the form that asks for one more address.
			network.FixedIPs = append(network.FixedIPs, dtgo.FixedIP{IPVersion: 4})
		}
		// An empty list means "no address", and such an instance boots unreachable.
		if len(network.FixedIPs) == 0 {
			network.FixedIPs = append(network.FixedIPs, dtgo.FixedIP{IPVersion: 4})
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
