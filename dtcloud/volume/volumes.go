// Package volume implements the dtcloud_volume resource and its data sources.
//
// A volume is a block device that exists on its own. Attaching one to a VM is a
// separate resource (dtcloud_vm_volume_attachment), because the attachment has
// its own lifecycle: a volume can be detached and re-attached elsewhere without
// the volume itself changing. This package owns creation, sizing and retyping,
// and never attaches or detaches.
//
// Two API behaviours shape the code here:
//
//   - The create paths answer with different shapes and different id keys. See
//     createdVolumeID.
//   - Extend and retype are accepted while the volume is at rest and it stays
//     at rest afterwards, still reporting the old value. Waits are therefore on
//     the size or the policy that was requested, never on the status alone.
package volume

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// noHTMLPattern mirrors the character rule the API applies to name and
// description. Enforcing it in the schema turns a rejected request halfway
// through an apply into an error during plan.
const noHTMLPattern = `^[^<>&"']*$`

// Size bounds the API accepts on create. Clone declares none, but the same
// range is applied to both, since a clone outside it fails later anyway.
const (
	minVolumeSizeGB = 1
	maxVolumeSizeGB = 8192
)

// volumeSettled reports whether a status means the platform has finished.
// `available` and `in-use` are the two resting states; everything else is in
// flight. An attached volume never passes through `available`, so omitting
// `in-use` here would hang any change made to a disk that is in use.
func volumeSettled(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "available", "in-use":
		return true
	}
	return false
}

// attachedToDescription names the machine holding a volume. Both the name and
// the id go in when both are reported; an attachment made outside Terraform
// sometimes carries only the id.
func attachedToDescription(v dtgo.GetVolumeDetails) string {
	switch {
	case v.AttachedTo != "" && v.AttachedToID != "":
		return fmt.Sprintf("%q (%s)", v.AttachedTo, v.AttachedToID)
	case v.AttachedTo != "":
		return fmt.Sprintf("%q", v.AttachedTo)
	case v.AttachedToID != "":
		return v.AttachedToID
	}
	return "a virtual machine"
}

// volumeFailed reports whether a status means the platform gave up. Matched as
// a substring because the failure statuses are a family (`error`,
// `error_deleting`, `error_extending`, ...) rather than a fixed set.
func volumeFailed(status string) bool {
	return strings.Contains(strings.ToLower(status), "error")
}

// parseSizeGB reads the size the list endpoint reports, which arrives as
// "20 GB" where the details endpoint sends the number 20.
func parseSizeGB(raw string) int {
	field := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "GB"))
	n, err := strconv.Atoi(strings.TrimSpace(field))
	if err != nil {
		return 0
	}
	return n
}

// parseBootable reads the API's stringly-typed boolean. Anything unparseable
// is false, since treating an unknown value as bootable would claim more than
// the API said.
func parseBootable(raw string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return b
}

// imageMetadataSchema describes where a volume's contents came from. Populated
// only for volumes built from an image; empty otherwise.
func imageMetadataSchema() *schema.Schema {
	return &schema.Schema{
		Type:        schema.TypeList,
		Computed:    true,
		Description: "Details of the image this volume was created from. Empty for a blank volume.",
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				"image_id":         {Type: schema.TypeString, Computed: true, Description: "ID of the source image."},
				"image_name":       {Type: schema.TypeString, Computed: true, Description: "Name of the source image."},
				"os_distro":        {Type: schema.TypeString, Computed: true, Description: "Distribution the image carries."},
				"os_type":          {Type: schema.TypeString, Computed: true, Description: "Operating system family."},
				"disk_format":      {Type: schema.TypeString, Computed: true, Description: "Disk format, e.g. qcow2 or iso."},
				"container_format": {Type: schema.TypeString, Computed: true, Description: "Container format of the image."},
				"checksum":         {Type: schema.TypeString, Computed: true, Description: "Checksum of the image data."},
				"min_disk":         {Type: schema.TypeString, Computed: true, Description: "Smallest disk the image will boot on, in GB."},
				"min_ram":          {Type: schema.TypeString, Computed: true, Description: "Smallest amount of RAM the image will boot with, in MB."},
				"size":             {Type: schema.TypeString, Computed: true, Description: "Size of the image data in bytes."},
			},
		},
	}
}

// flattenImageMetadata returns a one-element list, or an empty one when the
// volume was not built from an image.
//
// A blank volume decodes to the zero struct rather than to a nil pointer, so
// emptiness is inferred from the fields every image-backed volume carries.
func flattenImageMetadata(m dtgo.VolumeImageMetadata) []interface{} {
	if m.ImageID == "" && m.ImageName == "" && m.DiskFormat == "" {
		return []interface{}{}
	}
	return []interface{}{map[string]interface{}{
		"image_id":         m.ImageID,
		"image_name":       m.ImageName,
		"os_distro":        m.OsDistro,
		"os_type":          m.OsType,
		"disk_format":      m.DiskFormat,
		"container_format": m.ContainerFormat,
		"checksum":         m.Checksum,
		"min_disk":         m.MinDisk,
		"min_ram":          m.MinRAM,
		"size":             m.Size,
	}}
}

// waitForVolume blocks until the volume is at rest and `settled` agrees the
// requested change has landed.
//
// The second condition is the point of this function: after an extend or a
// retype the volume is still reported at rest with its old size or policy, so a
// status-only wait would succeed instantly and the next read would contradict
// the plan. Callers pass the value they asked for.
//
// A nil `settled` means any resting state will do, which is what create wants.
func waitForVolume(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration, settled func(dtgo.GetVolumeDetails) bool) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.Volume.GetVolumeDetails(ctx, id, nil)
			if err != nil {
				// A volume that has not appeared yet is not a failure; create
				// returns before the platform has committed it.
				if dterr.IsNotFound(err) {
					return "waiting", "waiting", nil
				}
				return nil, "", err
			}
			if details.ID == "" {
				return "waiting", "waiting", nil
			}
			if volumeFailed(details.Status) {
				return nil, "", fmt.Errorf("volume %q entered %s state", id, details.Status)
			}
			// Every other non-target status counts as pending. The transitional
			// states are deliberately not enumerated, so an unfamiliar one is
			// waited out rather than treated as an error.
			if !volumeSettled(details.Status) {
				return "waiting", "waiting", nil
			}
			if settled != nil && !settled(details) {
				return "waiting", "waiting", nil
			}
			return "done", "done", nil
		},
		Timeout:    timeout,
		Delay:      2 * time.Second,
		MinTimeout: 3 * time.Second,
		// Two readings in a row, so a poll landing between the action being
		// accepted and the volume leaving `available` cannot end the wait on
		// its own.
		ContinuousTargetOccurence: 2,
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// waitForVolumeGone blocks until the volume stops resolving, so destroy does
// not return while the platform is still tearing it down and the quota it
// occupies is still charged.
func waitForVolumeGone(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.Volume.GetVolumeDetails(ctx, id, nil)
			if err != nil {
				if dterr.IsNotFound(err) {
					return "done", "done", nil
				}
				return nil, "", err
			}
			if details.ID == "" {
				return "done", "done", nil
			}
			// A failure state during deletion is terminal; waiting out the full
			// timeout would only hide the reason.
			if volumeFailed(details.Status) {
				return nil, "", fmt.Errorf("volume %q entered %s state while being deleted", id, details.Status)
			}
			return "waiting", "waiting", nil
		},
		Timeout:    timeout,
		Delay:      2 * time.Second,
		MinTimeout: 3 * time.Second,
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}
