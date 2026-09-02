// Package volume implements the dtcloud_volume resource and its data sources.
//
// A volume is a block device that exists on its own. Attaching one to a VM is a
// separate resource (dtcloud_vm_volume_attachment), because the attachment has
// its own lifecycle: a volume can be detached and re-attached elsewhere without
// the volume itself changing. This package therefore never calls the attach or
// forceDetach routes — it owns creation, sizing and retyping, nothing else.
//
// Two things about this API are worth knowing before reading anything here.
//
// First, the two create paths answer differently. `POST /volumes` returns an
// object the client panel builds itself, keyed `volumeId`; `POST /{id}/clone`
// returns the raw OpenStack volume, keyed `id`. See createdVolumeID.
//
// Second, and more dangerous: `osExtend` and `osRetype` are accepted while the
// volume is `available` and it *stays* `available` for a while afterwards. A
// waiter that only watches the status returns immediately having done nothing —
// the same bug that shipped once on VM resize. So the waits here are on the
// value that was asked for, the size or the storage policy, and the status is
// only used to tell settled from in-flight.
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

// noHTMLPattern is the Joi rule the API applies to `name` and `description`:
// strictNoHtmlRegex = /^[^<>&"']*$/. Enforcing it in the schema turns a 406 in
// the middle of an apply into an error during plan.
const noHTMLPattern = `^[^<>&"']*$`

// Size bounds from createVolumeSchema. Clone declares no bounds, but a clone
// that ignores them would only fail deeper in OpenStack, so the same range is
// applied to both.
const (
	minVolumeSizeGB = 1
	maxVolumeSizeGB = 8192
)

// volumeSettled reports whether a status means the platform has finished.
//
// `available` and `in-use` are the two resting states — the route's own socket
// configuration uses exactly this pair as its success condition. Everything
// else is in flight.
func volumeSettled(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "available", "in-use":
		return true
	}
	return false
}

// attachedToDescription names the machine holding a volume, as well as the
// details response allows. The name is the useful half and the id is the
// searchable one, so both go in when both are reported; an attachment made
// outside Terraform sometimes carries only the id.
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

// volumeFailed reports whether a status means the platform gave up.
//
// Matched as a substring because the failure statuses are a family —
// `error`, `error_deleting`, `error_extending`, `error_restoring` — and
// enumerating them was the mistake that made the VM waiters fail on states
// nobody had listed.
func volumeFailed(status string) bool {
	return strings.Contains(strings.ToLower(status), "error")
}

// parseSizeGB reads the size the *list* endpoint reports.
//
// The list route builds it as `volume.size + ' GB'`, so it arrives as "20 GB"
// where the details route sends the number 20. dt-go types the two fields
// accordingly and leaves the difference to the caller.
func parseSizeGB(raw string) int {
	field := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "GB"))
	n, err := strconv.Atoi(strings.TrimSpace(field))
	if err != nil {
		return 0
	}
	return n
}

// parseBootable reads OpenStack's stringly-typed boolean. Anything unparseable
// is false, which is the safe reading: treating an unknown value as bootable
// would be a claim the API never made.
func parseBootable(raw string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return b
}

// imageMetadataSchema describes where a volume's contents came from. It is
// populated only for volumes built from an image; for a blank volume the
// details response omits the object and this stays an empty list.
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
// The details route sets `volume_image_metadata` from OpenStack and marks it
// omitempty in dt-go, so a blank volume decodes to the zero struct rather than
// to a nil pointer. Emptiness therefore has to be inferred, and ImageID is the
// field to infer it from: every image-backed volume has one.
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

// waitForVolume blocks until the volume is at rest and `settled` agrees that
// the change being waited for has actually landed.
//
// The second condition is the point of this function. After an extend or a
// retype the volume is still reported `available` with its old size or old
// policy for a while, so a status-only wait succeeds instantly and the next
// read then contradicts the plan. Callers pass the value they asked for and the
// wait ends only when the API reports it back.
//
// A nil `settled` means "any resting state will do", which is what create wants.
func waitForVolume(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration, settled func(dtgo.GetVolumeDetails) bool) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.Volume.GetVolumeDetails(ctx, id, nil)
			if err != nil {
				// A volume that has not appeared yet is not a failure; the
				// create call returns before OpenStack has committed it.
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
			// Every other non-target status counts as pending. Enumerating the
			// transitional ones is what broke the VM waiters: an unlisted state
			// failed with "unexpected state" instead of waiting for it.
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
		// Two readings in a row, so a poll that lands in the gap between the
		// action being accepted and the volume leaving `available` cannot end
		// the wait on its own.
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
			// error_deleting is terminal: the volume will not disappear on its
			// own, and waiting the full timeout only hides why.
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
