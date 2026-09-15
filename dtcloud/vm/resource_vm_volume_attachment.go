package vm

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

// ResourceDtcloudVMVolumeAttachment attaches an existing volume to a VM.
//
// Separate from dtcloud_vm because the attachment has its own lifecycle: a
// volume moves between machines without either changing. Creating a volume
// belongs to dtcloud_volume. The whole resource is ForceNew — attach and detach.
func ResourceDtcloudVMVolumeAttachment() *schema.Resource {
	return &schema.Resource{
		Description: "Attaches an existing volume to a virtual machine.",

		CreateContext: resourceDtcloudVMVolumeAttachmentCreate,
		ReadContext:   resourceDtcloudVMVolumeAttachmentRead,
		DeleteContext: resourceDtcloudVMVolumeAttachmentDelete,
		Importer: &schema.ResourceImporter{
			StateContext: resourceDtcloudVMVolumeAttachmentImport,
		},

		Schema: map[string]*schema.Schema{
			"vm_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the virtual machine to attach to.",
			},
			"volume_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the existing volume to attach.",
			},
			"volume_name": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Name of the attached volume.",
			},
			"size": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Size of the attached volume in GB.",
			},
			"storage_policy": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Storage policy / volume type of the attached volume.",
			},
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(10 * time.Minute),
			Delete: schema.DefaultTimeout(10 * time.Minute),
		},
	}
}

// volumeAttachmentID pairs the two ids, since the attachment has none of its
// own. The same shape is what `terraform import` expects.
func volumeAttachmentID(vmID, volumeID string) string {
	return fmt.Sprintf("%s:%s", vmID, volumeID)
}

func resourceDtcloudVMVolumeAttachmentCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	vmID := d.Get("vm_id").(string)
	volumeID := d.Get("volume_id").(string)

	if _, err := client.VirtualMachine.AttachExistingVolumeToVm(ctx, vmID, volumeID, nil); err != nil {
		return diag.Errorf("Error attaching volume %q to VM %q: %s", volumeID, vmID, err)
	}

	d.SetId(volumeAttachmentID(vmID, volumeID))

	// Attaching is asynchronous; wait until the volume shows up on the VM.
	if err := waitForVolumeAttachment(ctx, client, vmID, volumeID, true, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.Errorf("Error waiting for volume %q to attach to VM %q: %s", volumeID, vmID, err)
	}

	return resourceDtcloudVMVolumeAttachmentRead(ctx, d, meta)
}

func resourceDtcloudVMVolumeAttachmentRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	vmID := d.Get("vm_id").(string)
	volumeID := d.Get("volume_id").(string)

	volumes, _, err := client.VirtualMachine.GetVmVolumeAttachments(ctx, vmID, nil)
	if err != nil {
		// The VM being gone takes the attachment with it.
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error listing volumes on VM %q: %s", vmID, err)
	}

	for _, v := range volumes {
		if v.ID == volumeID {
			d.Set("volume_name", v.Name)
			d.Set("size", v.Size)
			d.Set("storage_policy", v.StoragePolicy)
			return nil
		}
	}

	// Detached outside Terraform.
	d.SetId("")
	return nil
}

func resourceDtcloudVMVolumeAttachmentDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	vmID := d.Get("vm_id").(string)
	volumeID := d.Get("volume_id").(string)

	if _, err := client.VirtualMachine.DetachVolumeFromVm(ctx, vmID, volumeID, nil); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error detaching volume %q from VM %q: %s", volumeID, vmID, err)
		}
	}

	if err := waitForVolumeDetached(ctx, client, volumeID, d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.Errorf("Error waiting for volume %q to detach from VM %q: %s", volumeID, vmID, err)
	}

	d.SetId("")
	return nil
}

func resourceDtcloudVMVolumeAttachmentImport(ctx context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	parts := strings.Split(d.Id(), ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("expected an id of the form <vm-id>:<volume-id>, got %q", d.Id())
	}
	d.Set("vm_id", parts[0])
	d.Set("volume_id", parts[1])
	d.SetId(volumeAttachmentID(parts[0], parts[1]))
	return []*schema.ResourceData{d}, nil
}

// waitForVolumeDetached watches the volume's own status until it is detached.
//
// The VM's volume list cannot be used for this. A detach is accepted with 202
// and then carried out by the guest: a running system that will not release the
// filesystem makes the platform put the volume back, and the sequence is
// `in-use` → `detaching` → `in-use` again, with the volume never leaving the
// VM's list. Watching the list, there is nothing to see — the wait runs to its
// timeout and reports a deadline, which says nothing about what happened or
// what to do about it.
//
// Coming back to `in-use` after `detaching` is therefore read as the refusal it
// is, and reported as soon as it is seen. Anything before the first `detaching`
// is not: the volume is still `in-use` for the moment between the request being
// accepted and the platform acting on it.
//
// This matters beyond the attachment itself. A volume cannot be deleted while
// it is attached, so every destroy of a managed volume that is attached to a
// running machine goes through here first.
func waitForVolumeDetached(ctx context.Context, client *dtgo.Client, volumeID string, timeout time.Duration) error {
	sawDetaching := false

	return waitForCondition(ctx, timeout, func() (bool, error) {
		volume, _, err := client.Volume.GetVolumeDetails(ctx, volumeID, nil)
		if err != nil {
			// A volume that no longer exists is not attached to anything.
			if dterr.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}

		switch status := strings.ToLower(volume.Status); status {
		case "detaching":
			sawDetaching = true
			return false, nil
		case "in-use":
			if !sawDetaching {
				return false, nil
			}
			return false, fmt.Errorf(
				"the platform put the volume back: it went `detaching` and returned to `in-use`, "+
					"which is how a detach the guest refuses ends. The usual cause is the "+
					"filesystem still being mounted on VM %q. Unmount it in the guest, or stop "+
					"the machine, then apply again",
				volume.AttachedTo)
		case "error_detaching":
			return false, fmt.Errorf("the volume is in `error_detaching`; the platform could not complete the detach")
		default:
			// `available` is the finished state; the rest — `deleting`, say —
			// are not attachments either, and none of them is this resource's
			// to wait on.
			return true, nil
		}
	})
}

// waitForVolumeAttachment polls the VM's volume list until the volume is
// present (want=true) or gone (want=false).
func waitForVolumeAttachment(ctx context.Context, client *dtgo.Client, vmID, volumeID string, want bool, timeout time.Duration) error {
	return waitForCondition(ctx, timeout, func() (bool, error) {
		volumes, _, err := client.VirtualMachine.GetVmVolumeAttachments(ctx, vmID, nil)
		if err != nil {
			// A detach that removed the VM as well counts as done.
			if !want && dterr.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		for _, v := range volumes {
			if v.ID == volumeID {
				return want, nil
			}
		}
		return !want, nil
	})
}
