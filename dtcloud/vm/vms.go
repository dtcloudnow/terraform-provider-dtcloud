package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
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
	// stateShelved releases the instance's compute resources while keeping the
	// VM and its disks. It is a persistent state rather than an operation, which
	// is why it belongs here alongside running and stopped.
	stateShelved = "shelved"
)

// Actions accepted by POST /vms/{id}/actions that this provider drives.
const (
	actionStart    = "start"
	actionSoftStop = "softStop"
	actionHardStop = "hardStop"
	actionResize   = "resize"
	actionShelve   = "shelve"
	actionUnshelve = "unshelve"
)

// powerStateOf maps an API status onto the `state` argument's vocabulary.
// Anything transitional maps to running, because a VM that is not deliberately
// stopped is on its way to being usable.
func powerStateOf(status string) string {
	switch status {
	case statusShutoff:
		return stateStopped
	case statusShelved, statusShelvedOff:
		return stateShelved
	}
	return stateRunning
}

// vmComputedSchema is the set of attributes the API reports back about a VM.
// It is shared by the resource and the data source.
//
// `network_interface` and `volume` come from separate endpoints
// (`GET /vms/{id}/networks` and `/volumes`) rather than from the details
// response, which is why they are gathered by readVMAttachments.
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

// setVMAttributes copies an API response onto the Terraform state.
// vmDetailsExtras is the part of the details response dt-go's typed struct
// leaves out.
//
// `hotPlugEnabled` matters: enable_hot_plug is an argument users can change in
// place, so without reading it back a change made outside Terraform stays
// invisible and an import lands on the schema default. Every dt-go method also
// returns the raw body, so it is decoded here rather than changing the SDK.
type vmDetailsExtras struct {
	HotPlugEnabled *bool             `json:"hotPlugEnabled"`
	Metadata       map[string]string `json:"metadata"`
}

// setVMExtras reads what setVMAttributes cannot. A body that will not decode is
// not an error — the rest of the read is still good, the extras just stay as
// they were.
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

// readVMAttachments fills in the network interfaces and volumes, which live
// behind their own endpoints rather than in the details response.
//
// Neither is fatal: a VM that is up but whose attachments cannot be listed is
// still usable, and failing the whole Read would take the resource out of state
// over a secondary lookup. Errors are returned so callers can surface them as
// warnings.
func readVMAttachments(ctx context.Context, client *dtgo.Client, d *schema.ResourceData, vmID string) []error {
	var errs []error

	interfaces, _, err := client.VirtualMachine.GetVmNetworkInterfaces(ctx, vmID, nil)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing network interfaces: %w", err))
	} else {
		d.Set("network_interface", flattenNetworkInterfaces(interfaces))
		d.Set("primary_ip", primaryIPOf(interfaces))
		recoverNetworkBlocks(d, interfaces)
	}

	volumes, _, err := client.VirtualMachine.GetVmVolumeAttachments(ctx, vmID, nil)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing volume attachments: %w", err))
	} else {
		d.Set("volume", flattenVolumeAttachments(volumes))
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

// primaryIPOf picks the address most callers want to reference: the first
// public interface's primary IP, or the first interface that has one at all.
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

// formatTime renders an API timestamp for state. A timestamp that was absent,
// null or in a format dtgo could not parse reads as the empty string rather than
// a misleading zero date.
func formatTime(t dtgo.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// waitForVMStatus blocks until the VM settles into one of target, or fails.
//
// Creating and renaming a VM are asynchronous: the API answers immediately and
// the platform then polls OpenStack itself (it drives a websocket with a
// one-second interval and a 120-attempt cap). Terraform has no websocket, so it
// polls the details endpoint the same way.
//
// A VM that reaches ERROR is reported as a failure rather than being waited on
// until timeout, which is the difference between a 30-second error and a
// 20-minute one.
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
		// Anything that is not the target counts as "still working". Listing
		// the transitional states explicitly was a mistake: a VM being stopped
		// stays ACTIVE for a while, and an omitted state makes StateChangeConf
		// fail immediately with "unexpected state" instead of waiting. Only
		// ERROR short-circuits.
		Pending: []string{"pending"},
		Target:  []string{"target"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.VirtualMachine.GetVirtualMachineDetails(ctx, vmID, nil)
			if err != nil {
				// A VM that is still being registered can 404 briefly; keep
				// polling rather than failing the whole apply.
				if dterr.IsNotFound(err) {
					return nil, "", nil
				}
				return nil, "", err
			}
			if details.Status == statusError && !isTarget(statusError) {
				return details, "", fmt.Errorf("VM %s entered ERROR state", vmID)
			}
			if isTarget(details.Status) {
				return details, "target", nil
			}
			return details, "pending", nil
		},
		Timeout:    timeout,
		Delay:      5 * time.Second,
		MinTimeout: 3 * time.Second,
		// Require the target twice in a row. The details endpoint was observed
		// briefly reporting the pre-transition status right after an action
		// completed, which left the `status` attribute one step stale in state
		// until the next refresh. Two consecutive sightings smooth that over.
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

// waitForVMGone blocks until the VM stops resolving, so that destroy does not
// return while the platform is still tearing the instance down.
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
			// An id that no longer resolves comes back empty rather than as a
			// 404 on some paths, so treat that as gone too.
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

// setPowerState drives the VM to the requested state and waits for it to
// settle. It is a no-op when the VM is already there.
//
// Leaving the shelved state is a two-step move: a shelved VM has to be brought
// back before it can be stopped, so shelved -> stopped unshelves first.
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

// resizeVM moves the VM onto a different flavor.
//
// Two platform behaviours shape this:
//
//   - **The VM has to be stopped.** Resizing a running instance is refused, so
//     this stops it first and leaves it stopped; the caller restores the desired
//     power state afterwards.
//   - **VERIFY_RESIZE settles on its own.** The platform parks the VM there
//     briefly and then moves on without anyone confirming.
//
// The completion check is the *flavor*, not the status. Waiting on status alone
// is wrong here and was a real bug: the VM is already SHUTOFF when the resize is
// requested, so a wait for "ACTIVE or SHUTOFF" returns immediately, before the
// resize has even started. Waiting until the VM reports the flavor that was
// asked for — and has settled — is the only condition that actually means done.
// resizeVM changes a VM's flavor.
//
// The platform normally refuses to resize a running instance, so the VM is
// stopped first and the caller puts it back into the configured power state
// afterwards. Hot plug is the exception: with it enabled the instance takes
// vCPU and memory changes while ACTIVE, so stopping it would be a pointless
// outage.
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
			return false, fmt.Errorf("VM %s entered ERROR state during resize", vmID)
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

// waitForCondition polls check until it reports done, the context is cancelled
// or timeout elapses. It exists because attach/detach have no status field to
// watch — the only signal is whether the resource appears in a list — so
// StateChangeConf's state vocabulary would be noise here.
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

// resolveFlavorID maps the flavor *name* the details endpoint reports back to a
// flavor id, which is what the schema stores.
//
// `GET /vms/{id}/details` returns `flavor: {name, vcpus, ram}` with no id, so
// without this Read could not populate flavor_id at all: imports would come out
// blank and a resize performed outside Terraform would never show up in a plan.
//
// Names are not guaranteed unique. When the lookup is ambiguous or finds
// nothing, this returns "" and the caller keeps whatever is already in state —
// better a stale value than a wrong one, and far better than clearing the field
// and provoking a spurious resize.
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

// recoverNetworkBlocks fills in the `network` blocks after an import.
//
// The blocks are ForceNew and the create call is the only place they are ever
// sent, so an import that leaves them empty makes the first plan propose to
// destroy the VM it has just adopted. They are recoverable after all: the
// interface list reports the network id, the security group ids, and — as
// `spoofingProtection` — port security.
//
// Only on import. During normal operation the blocks already hold what the
// configuration asked for, and overwriting them with what the platform reports
// would invent differences: a fixed_ip the user pinned versus one the subnet
// allocated is the obvious one, and any difference here means a rebuild.
//
// fixed_ip is reconstructed as an unpinned IPv4 request rather than with the
// address the interface actually holds, because that is what the overwhelming
// majority of configurations say. Pin an address and the import will want a
// rebuild — write the block to match before applying.
func recoverNetworkBlocks(d *schema.ResourceData, interfaces dtgo.GetVmNetworkInterfaces) {
	// readVMAttachments is shared with the data source, whose schema has no
	// `network` at all — so this has to cope with the field being absent, not
	// merely empty. A bare type assertion panicked here.
	existing, ok := d.Get("network").([]interface{})
	if !ok || len(existing) > 0 {
		return
	}

	blocks := make([]interface{}, 0, len(interfaces))
	for _, iface := range interfaces {
		groups := make([]interface{}, 0, len(iface.SecurityGroups))
		for _, g := range iface.SecurityGroups {
			groups = append(groups, g.ID)
		}
		blocks = append(blocks, map[string]interface{}{
			"uuid":                  iface.ID,
			"security_groups":       groups,
			"port_security_enabled": iface.SpoofingProtection,
			"fixed_ip":              []interface{}{map[string]interface{}{"ip_version": 4}},
		})
	}
	d.Set("network", blocks)
}
