package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// VM lifecycle states reported by the API.
const (
	statusActive       = "ACTIVE"
	statusShutoff      = "SHUTOFF"
	statusError        = "ERROR"
	statusBuild        = "BUILD"
	statusVerifyResize = "VERIFY_RESIZE"
	statusShelved      = "SHELVED"
	statusShelvedOff   = "SHELVED_OFFLOADED"
)

// Desired power states exposed as the `state` argument.
const (
	stateRunning = "running"
	stateStopped = "stopped"
	// stateShelved releases compute while keeping the VM and its disks. A
	// persistent state rather than an operation, which is why it belongs here.
	stateShelved = "shelved"
)

// Actions this provider drives.
const (
	actionStart    = "start"
	actionSoftStop = "softStop"
	actionHardStop = "hardStop"
	actionResize   = "resize"
	actionShelve   = "shelve"
	actionUnshelve = "unshelve"
)

// powerStateOf maps a reported status onto the `state` argument's vocabulary.
// Anything transitional maps to running.
func powerStateOf(status string) string {
	switch status {
	case statusShutoff:
		return stateStopped
	case statusShelved, statusShelvedOff:
		return stateShelved
	}
	return stateRunning
}

// vmComputedSchema is what the API reports back about a VM, shared by the
// resource and the data source.
func vmComputedSchema() map[string]*schema.Schema {
	return map[string]*schema.Schema{
		"network_interface": {
			Type:        schema.TypeList,
			Computed:    true,
			Description: "Network interfaces currently attached to the VM.",
			Elem: &schema.Resource{
				Schema: map[string]*schema.Schema{
					"port_id":             {Type: schema.TypeString, Computed: true, Description: "ID of the port, used to update or detach this interface."},
					"network_id":          {Type: schema.TypeString, Computed: true, Description: "ID of the network the interface is on."},
					"network_name":        {Type: schema.TypeString, Computed: true, Description: "Name of the network."},
					"mac_address":         {Type: schema.TypeString, Computed: true, Description: "MAC address of the interface."},
					"primary_ip":          {Type: schema.TypeString, Computed: true, Description: "Primary IP address on this interface."},
					"secondary_ips":       {Type: schema.TypeList, Computed: true, Elem: &schema.Schema{Type: schema.TypeString}, Description: "Additional IP addresses on this interface."},
					"is_public":           {Type: schema.TypeBool, Computed: true, Description: "Whether the interface is on a public network."},
					"spoofing_protection": {Type: schema.TypeBool, Computed: true, Description: "Whether port security is enforced on this interface."},
					"security_groups": {
						Type:     schema.TypeList,
						Computed: true,
						Elem: &schema.Resource{
							Schema: map[string]*schema.Schema{
								"id":   {Type: schema.TypeString, Computed: true},
								"name": {Type: schema.TypeString, Computed: true},
							},
						},
						Description: "Security groups bound to this interface.",
					},
				},
			},
		},
		"volume": {
			Type:        schema.TypeList,
			Computed:    true,
			Description: "Volumes currently attached to the VM, including the boot disk.",
			Elem: &schema.Resource{
				Schema: map[string]*schema.Schema{
					"id":                    {Type: schema.TypeString, Computed: true, Description: "ID of the volume."},
					"name":                  {Type: schema.TypeString, Computed: true, Description: "Name of the volume."},
					"storage_policy":        {Type: schema.TypeString, Computed: true, Description: "Storage policy / volume type."},
					"size":                  {Type: schema.TypeInt, Computed: true, Description: "Size in GB."},
					"delete_on_termination": {Type: schema.TypeBool, Computed: true, Description: "Whether the volume is deleted with the VM."},
				},
			},
		},
		"primary_ip": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "Convenience shortcut: the primary IP of the first public interface, falling back to the first interface with an address.",
		},
		"status": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "Current power/provisioning state, e.g. ACTIVE, SHUTOFF, ERROR.",
		},
		"task_state": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "In-flight OpenStack task, empty when the VM is settled.",
		},
		"image": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "Name of the image the VM was built from.",
		},
		"image_os_type": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "OS family of the image, e.g. linux or windows.",
		},
		"ssh_key": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "Name of the SSH key injected into the VM.",
		},
		"flavor_name": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "Human-readable name of the VM's flavor.",
		},
		"vcpus": {
			Type:        schema.TypeInt,
			Computed:    true,
			Description: "Number of virtual CPUs.",
		},
		"ram": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "Memory as reported by the API, e.g. \"512 MB\".",
		},
		"created_at": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "When the VM was created, in RFC 3339 format.",
		},
		"last_modified": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "When the VM was last modified, in RFC 3339 format.",
		},
	}
}

// vmDetailsExtras is the part of the details response the typed struct leaves
// out. `hotPlugEnabled` changes in place, so an unread change stays invisible.
type vmDetailsExtras struct {
	HotPlugEnabled *bool             `json:"hotPlugEnabled"`
	Metadata       map[string]string `json:"metadata"`
}

// setVMExtras reads what setVMAttributes cannot. A body that will not decode
// leaves the extras as they were rather than failing the read.
func setVMExtras(d *schema.ResourceData, body string) {
	var extras vmDetailsExtras
	if err := json.Unmarshal([]byte(body), &extras); err != nil {
		return
	}
	if extras.HotPlugEnabled != nil {
		d.Set("enable_hot_plug", *extras.HotPlugEnabled)
	}
	d.Set("metadata", extras.Metadata)
}

func setVMAttributes(d *schema.ResourceData, details dtgo.GetVirtualMachineDetails) {
	d.Set("name", details.Name)
	d.Set("status", details.Status)
	d.Set("task_state", details.TaskState)
	d.Set("image", details.Image)
	d.Set("image_os_type", details.ImageOsType)
	d.Set("ssh_key", details.SSHKey)
	d.Set("flavor_name", details.Flavor.Name)
	d.Set("vcpus", details.Flavor.VCpus)
	d.Set("ram", details.Flavor.RAM)
	d.Set("created_at", formatTime(details.CreationTime))
	d.Set("last_modified", formatTime(details.LastModified))
}

// readVMAttachments fills in the network interfaces and volumes from their own
// endpoints. Errors come back for the caller to warn on: failing the whole Read
// would drop the resource from state.
func readVMAttachments(ctx context.Context, client *dtgo.Client, d *schema.ResourceData, vmID, imageID string) []error {
	var errs []error

	interfaces, _, err := client.VirtualMachine.GetVmNetworkInterfaces(ctx, vmID, nil)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing network interfaces: %w", err))
	} else {
		d.Set("network_interface", flattenNetworkInterfaces(interfaces))
		d.Set("primary_ip", primaryIPOf(interfaces))
		refreshNetworkBlocks(d, interfaces)
	}

	volumes, _, err := client.VirtualMachine.GetVmVolumeAttachments(ctx, vmID, nil)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing volume attachments: %w", err))
	} else {
		d.Set("volume", flattenVolumeAttachments(volumes))
		refreshBlockDevices(d, volumes, imageID)
	}

	return errs
}

func flattenNetworkInterfaces(interfaces dtgo.GetVmNetworkInterfaces) []interface{} {
	out := make([]interface{}, 0, len(interfaces))
	for _, iface := range interfaces {
		groups := make([]interface{}, 0, len(iface.SecurityGroups))
		for _, g := range iface.SecurityGroups {
			groups = append(groups, map[string]interface{}{"id": g.ID, "name": g.Name})
		}
		secondary := make([]interface{}, 0, len(iface.SecondaryIps))
		for _, ip := range iface.SecondaryIps {
			secondary = append(secondary, ip)
		}
		out = append(out, map[string]interface{}{
			"port_id":             iface.PortID,
			"network_id":          iface.ID,
			"network_name":        iface.NetworkName,
			"mac_address":         iface.MacAddress,
			"primary_ip":          iface.PrimaryIP,
			"secondary_ips":       secondary,
			"is_public":           iface.IsPublic,
			"spoofing_protection": iface.SpoofingProtection,
			"security_groups":     groups,
		})
	}
	return out
}

func flattenVolumeAttachments(volumes dtgo.GetVmVolumeAttachments) []interface{} {
	out := make([]interface{}, 0, len(volumes))
	for _, v := range volumes {
		out = append(out, map[string]interface{}{
			"id":                    v.ID,
			"name":                  v.Name,
			"storage_policy":        v.StoragePolicy,
			"size":                  v.Size,
			"delete_on_termination": v.DeleteOnTermination,
		})
	}
	return out
}

// matchVolumesToBlockDevices returns, per block_device, an index into volumes or
// -1. Past the boot volume there is no ordering, so a device is claimed only
// when exactly one unused volume matches the recorded size and policy.
func matchVolumesToBlockDevices(devices []interface{}, volumes dtgo.GetVmVolumeAttachments) []int {
	used := make([]bool, len(volumes))
	matched := make([]int, len(devices))

	for i := range devices {
		matched[i] = -1
	}
	if len(devices) > 0 && len(volumes) > 0 {
		matched[0] = 0
		used[0] = true
	}

	for i := 1; i < len(devices); i++ {
		device, ok := devices[i].(map[string]interface{})
		if !ok {
			continue
		}
		size, _ := device["volume_size"].(int)
		policy, _ := device["volume_type"].(string)

		candidate := -1
		for j := range volumes {
			if used[j] || volumes[j].Size != size || volumes[j].StoragePolicy != policy {
				continue
			}
			if candidate >= 0 {
				candidate = -1 // ambiguous: two volumes look identical
				break
			}
			candidate = j
		}
		if candidate >= 0 {
			used[candidate] = true
			matched[i] = candidate
		}
	}
	return matched
}

// refreshBlockDevices writes back what is reported about the boot-time disks, so
// drift shows in a plan and an import does not propose a rebuild. Unreported
// fields are filled in on an import only, and only for the boot device.
func refreshBlockDevices(d *schema.ResourceData, volumes dtgo.GetVmVolumeAttachments, imageID string) {
	// Shared with the data source, which has no block_device at all.
	existing, ok := d.Get("block_device").([]interface{})
	if !ok {
		return
	}

	if len(existing) == 0 {
		if len(volumes) == 0 {
			return
		}
		boot := volumes[0]
		device := map[string]interface{}{
			"boot_index":            0,
			"volume_size":           boot.Size,
			"volume_type":           boot.StoragePolicy,
			"delete_on_termination": boot.DeleteOnTermination,
			"destination_type":      "volume",
			"device_type":           "disk",
			"source_type":           "blank",
		}
		if imageID != "" {
			device["uuid"] = imageID
			device["source_type"] = "image"
		}
		d.Set("block_device", []interface{}{device})
		return
	}

	matched := matchVolumesToBlockDevices(existing, volumes)
	devices := make([]interface{}, 0, len(existing))
	for i, raw := range existing {
		device, ok := raw.(map[string]interface{})
		if !ok {
			devices = append(devices, raw)
			continue
		}
		updated := make(map[string]interface{}, len(device))
		for k, v := range device {
			updated[k] = v
		}
		if j := matched[i]; j >= 0 {
			updated["volume_size"] = volumes[j].Size
			updated["volume_type"] = volumes[j].StoragePolicy
			updated["delete_on_termination"] = volumes[j].DeleteOnTermination
			if i == 0 && imageID != "" {
				updated["uuid"] = imageID
			}
		}
		devices = append(devices, updated)
	}
	d.Set("block_device", devices)
}

// faultSuffix renders the explanation for an ERROR state. Without it the
// provider can only say a VM failed.
func faultSuffix(fault string) string {
	if fault == "" {
		return ""
	}
	return ": " + fault
}

// primaryIPOf picks the first public interface's primary IP, or the first
// interface that has one at all.
func primaryIPOf(interfaces dtgo.GetVmNetworkInterfaces) string {
	fallback := ""
	for _, iface := range interfaces {
		if iface.PrimaryIP == "" {
			continue
		}
		if iface.IsPublic {
			return iface.PrimaryIP
		}
		if fallback == "" {
			fallback = iface.PrimaryIP
		}
	}
	return fallback
}

// formatTime renders a timestamp for state. An absent or unparsable one reads
// as empty rather than as a misleading zero date.
func formatTime(t dtgo.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// waitForVMStatus blocks until the VM settles into one of target. ERROR fails
// immediately rather than waiting out the timeout.
func waitForVMStatus(ctx context.Context, client *dtgo.Client, vmID string, target []string, timeout time.Duration) (dtgo.GetVirtualMachineDetails, error) {
	isTarget := func(status string) bool {
		for _, t := range target {
			if status == t {
				return true
			}
		}
		return false
	}

	stateConf := &retry.StateChangeConf{
		// Anything that is not the target counts as still working: listing the
		// transitional states instead fails on any that was omitted.
		Pending: []string{"pending"},
		Target:  []string{"target"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.VirtualMachine.GetVirtualMachineDetails(ctx, vmID, nil)
			if err != nil {
				// A VM that is still being registered can 404 briefly.
				if dterr.IsNotFound(err) {
					return nil, "", nil
				}
				return nil, "", err
			}
			if details.Status == statusError && !isTarget(statusError) {
				return details, "", fmt.Errorf("VM %s entered ERROR state%s", vmID, faultSuffix(details.Fault))
			}
			if isTarget(details.Status) {
				return details, "target", nil
			}
			return details, "pending", nil
		},
		Timeout:    timeout,
		Delay:      5 * time.Second,
		MinTimeout: 3 * time.Second,
		// Require the target twice: the status can briefly read as the pre-transition
		// one right after an action completes.
		ContinuousTargetOccurence: 2,
	}

	raw, err := stateConf.WaitForStateContext(ctx)
	if err != nil {
		return dtgo.GetVirtualMachineDetails{}, err
	}
	details, ok := raw.(dtgo.GetVirtualMachineDetails)
	if !ok {
		return dtgo.GetVirtualMachineDetails{}, fmt.Errorf("unexpected response while waiting for VM %s", vmID)
	}
	return details, nil
}

// waitForVMGone blocks until the VM stops resolving, so destroy does not return
// while the instance is still being torn down.
func waitForVMGone(ctx context.Context, client *dtgo.Client, vmID string, timeout time.Duration) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"present"},
		Target:  []string{"gone"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.VirtualMachine.GetVirtualMachineDetails(ctx, vmID, nil)
			if err != nil {
				if dterr.IsNotFound(err) {
					return "gone", "gone", nil
				}
				return nil, "", err
			}
			// An id that no longer resolves comes back empty on some paths.
			if details.ID == "" {
				return "gone", "gone", nil
			}
			return details, "present", nil
		},
		Timeout:    timeout,
		Delay:      3 * time.Second,
		MinTimeout: 3 * time.Second,
	}

	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// setPowerState drives the VM to the requested state and waits for it to settle.
// Leaving the shelved state takes two steps: shelved -> stopped unshelves first.
func setPowerState(ctx context.Context, client *dtgo.Client, vmID, desired string, graceful bool, timeout time.Duration) error {
	details, _, err := client.VirtualMachine.GetVirtualMachineDetails(ctx, vmID, nil)
	if err != nil {
		return err
	}
	current := powerStateOf(details.Status)
	if current == desired {
		return nil
	}

	if current == stateShelved {
		if err := runPowerAction(ctx, client, vmID, actionUnshelve, []string{statusActive, statusShutoff}, timeout); err != nil {
			return fmt.Errorf("unshelving: %w", err)
		}
		if desired == stateRunning {
			return nil
		}
		details, _, err = client.VirtualMachine.GetVirtualMachineDetails(ctx, vmID, nil)
		if err != nil {
			return err
		}
		if powerStateOf(details.Status) == desired {
			return nil
		}
	}

	action := actionStart
	target := []string{statusActive}
	switch desired {
	case stateStopped:
		action = actionSoftStop
		if !graceful {
			action = actionHardStop
		}
		target = []string{statusShutoff}
	case stateShelved:
		action = actionShelve
		target = []string{statusShelved, statusShelvedOff}
	}

	if err := runPowerAction(ctx, client, vmID, action, target, timeout); err != nil {
		return fmt.Errorf("moving the VM to %s: %w", desired, err)
	}
	return nil
}

func runPowerAction(ctx context.Context, client *dtgo.Client, vmID, action string, target []string, timeout time.Duration) error {
	if _, err := client.VirtualMachine.PerformVmAction(ctx, vmID, dtgo.PerformVmActionParams{Action: action}, nil); err != nil {
		return fmt.Errorf("performing %s: %w", action, err)
	}
	if _, err := waitForVMStatus(ctx, client, vmID, target, timeout); err != nil {
		return fmt.Errorf("waiting after %s: %w", action, err)
	}
	return nil
}

// resizeVM changes a VM's flavor. A running instance is stopped first and the
// caller restores the configured power state; with hot plug it takes the change
// while ACTIVE. The completion check is the flavor, not the status — the VM is
// already SHUTOFF, so a status wait would return before anything happened.
func resizeVM(ctx context.Context, client *dtgo.Client, vmID, flavorID string, graceful, hotPlug bool, timeout time.Duration) error {
	if !hotPlug {
		if err := setPowerState(ctx, client, vmID, stateStopped, graceful, timeout); err != nil {
			return fmt.Errorf("stopping the VM before resize: %w", err)
		}
	}

	if _, err := client.VirtualMachine.PerformVmAction(ctx, vmID, dtgo.PerformVmActionParams{
		Action:    actionResize,
		FlavorRef: flavorID,
	}, nil); err != nil {
		return fmt.Errorf("requesting resize: %w", err)
	}

	err := waitForCondition(ctx, timeout, func() (bool, error) {
		details, _, err := client.VirtualMachine.GetVirtualMachineDetails(ctx, vmID, nil)
		if err != nil {
			return false, err
		}
		if details.Status == statusError {
			return false, fmt.Errorf("VM %s entered ERROR state during resize%s", vmID, faultSuffix(details.Fault))
		}
		if details.Status != statusActive && details.Status != statusShutoff {
			return false, nil
		}
		return resolveFlavorID(ctx, client, details.Flavor.Name) == flavorID, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for the resize to settle: %w", err)
	}
	return nil
}

// waitForCondition polls check until it reports done. Attach and detach have no
// status to watch; the only signal is whether the resource appears in a list.
func waitForCondition(ctx context.Context, timeout time.Duration, check func() (bool, error)) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			done, err := check()
			if err != nil {
				return nil, "", err
			}
			if done {
				return "done", "done", nil
			}
			return "waiting", "waiting", nil
		},
		Timeout:    timeout,
		Delay:      2 * time.Second,
		MinTimeout: 2 * time.Second,
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// resolveFlavorID maps the reported flavor name back to the id the schema
// stores. Names are not unique: an ambiguous or missing lookup returns "" and
// the caller keeps what state holds, since clearing it would provoke a resize.
func resolveFlavorID(ctx context.Context, client *dtgo.Client, flavorName string) string {
	if flavorName == "" {
		return ""
	}
	flavors, _, err := client.Flavor.ListFlavors(ctx, nil)
	if err != nil {
		return ""
	}
	match := ""
	for _, f := range flavors {
		if f.Name != flavorName {
			continue
		}
		if match != "" {
			return "" // ambiguous
		}
		match = f.ID
	}
	return match
}

// matchPortsToNetworkBlocks returns, per `network` block, an index into
// interfaces, or -1 when no port matches. Matching is by network id, not
// position: ports come back in no particular order, and a positional read would
// rewrite the list every refresh, which reads as "replace the VM".
func matchPortsToNetworkBlocks(blocks []interface{}, interfaces dtgo.GetVmNetworkInterfaces) []int {
	used := make([]bool, len(interfaces))
	matched := make([]int, len(blocks))

	for i, raw := range blocks {
		matched[i] = -1
		block, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		wanted, _ := block["uuid"].(string)
		if wanted == "" {
			continue
		}
		for j := range interfaces {
			if !used[j] && interfaces[j].ID == wanted {
				used[j] = true
				matched[i] = j
				break
			}
		}
	}
	return matched
}

// portAddresses returns the addresses on a port, primary first. Empty entries
// are dropped: a port with no address decodes as a single empty string.
func portAddresses(interfaces dtgo.GetVmNetworkInterfaces, i int) []string {
	iface := interfaces[i]
	addresses := make([]string, 0, 1+len(iface.SecondaryIps))
	if iface.PrimaryIP != "" {
		addresses = append(addresses, iface.PrimaryIP)
	}
	for _, ip := range iface.SecondaryIps {
		if ip != "" {
			addresses = append(addresses, ip)
		}
	}
	return addresses
}

// ipVersionOf infers 4 or 6 from the address, which is not reported.
func ipVersionOf(address string) int {
	if strings.Contains(address, ":") {
		return 6
	}
	return 4
}

func fixedIPBlocksOf(addresses []string) []interface{} {
	blocks := make([]interface{}, 0, len(addresses))
	for _, ip := range addresses {
		blocks = append(blocks, map[string]interface{}{
			"ip_address": ip,
			"ip_version": ipVersionOf(ip),
		})
	}
	return blocks
}

func securityGroupIDsOf(interfaces dtgo.GetVmNetworkInterfaces, i int) []interface{} {
	groups := interfaces[i].SecurityGroups
	ids := make([]interface{}, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.ID)
	}
	return ids
}

func refreshNetworkBlocks(d *schema.ResourceData, interfaces dtgo.GetVmNetworkInterfaces) {
	// Shared with the data source, which has no `network` at all, so the field can
	// be absent rather than merely empty.
	existing, ok := d.Get("network").([]interface{})
	if !ok {
		return
	}

	if len(existing) == 0 {
		blocks := make([]interface{}, 0, len(interfaces))
		for i := range interfaces {
			blocks = append(blocks, map[string]interface{}{
				"uuid":                  interfaces[i].ID,
				"security_groups":       securityGroupIDsOf(interfaces, i),
				"port_security_enabled": interfaces[i].SpoofingProtection,
				"fixed_ip":              fixedIPBlocksOf(portAddresses(interfaces, i)),
			})
		}
		d.Set("network", blocks)
		return
	}

	matched := matchPortsToNetworkBlocks(existing, interfaces)
	blocks := make([]interface{}, 0, len(existing))
	for i, raw := range existing {
		block, ok := raw.(map[string]interface{})
		if !ok {
			blocks = append(blocks, raw)
			continue
		}
		updated := make(map[string]interface{}, len(block))
		for k, v := range block {
			updated[k] = v
		}
		if j := matched[i]; j >= 0 {
			updated["uuid"] = interfaces[j].ID
			updated["security_groups"] = securityGroupIDsOf(interfaces, j)
			updated["port_security_enabled"] = interfaces[j].SpoofingProtection
			updated["fixed_ip"] = fixedIPBlocksOf(portAddresses(interfaces, j))
		}
		blocks = append(blocks, updated)
	}
	d.Set("network", blocks)
}

// rawBool reads a top-level optional bool from the raw configuration, nil when
// unset. d.Get reads an unset bool and an explicit false the same way.
func rawBool(d *schema.ResourceData, key string) *bool {
	raw := d.GetRawConfig()
	if raw.IsNull() || !raw.IsKnown() {
		return nil
	}
	v := raw.GetAttr(key)
	if v.IsNull() || !v.IsKnown() {
		return nil
	}
	b := v.True()
	return &b
}
