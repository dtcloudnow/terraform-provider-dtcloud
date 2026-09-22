// Package volume implements the dtcloud_volume resource and its data sources.
//
// A volume exists on its own; attaching one to a VM is
// dtcloud_vm_volume_attachment, which has its own lifecycle since a volume moves
// between machines without changing. This package owns creation, sizing and
// retyping only.
//
// Two API behaviours shape it: the create paths answer with different id keys
// (see createdVolumeID), and extend and retype leave the volume at rest still
// reporting the old value, so waits compare the value rather than the status.
package volume

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/wait"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// noHTMLPattern mirrors the character rule on name and description, so a
// request rejected halfway through an apply becomes an error during plan.
const noHTMLPattern = `^[^<>&"']*$`

// Size bounds create accepts. Clone declares none, but the same range applies
// to both, since a clone outside it fails later anyway.
const (
	minVolumeSizeGB = 1
	maxVolumeSizeGB = 8192
)

// volumeSettled reports whether a status is at rest. An attached volume never
// passes through `available`, so omitting `in-use` would hang a disk in use.
func volumeSettled(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "available", "in-use":
		return true
	}
	return false
}

// attachedToDescription names the machine holding a volume. An attachment made
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

// volumeFailed reports whether a status means the platform gave up. Matched as
// a substring: the failure statuses are a family, not a fixed set.
func volumeFailed(status string) bool {
	return strings.Contains(strings.ToLower(status), "error")
}

// parseSizeGB reads the size the list endpoint reports as "20 GB", where the
// details endpoint sends the number 20.
func parseSizeGB(raw string) int {
	field := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "GB"))
	n, err := strconv.Atoi(strings.TrimSpace(field))
	if err != nil {
		return 0
	}
	return n
}

// parseBootable reads the stringly-typed boolean. Anything unparseable reads as
// false, since treating an unknown value as bootable claims more than was said.
func parseBootable(raw string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return b
}

// imageMetadataSchema describes where a volume's contents came from, and is
// populated only for volumes built from an image.
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
// volume was not built from an image. A blank volume decodes to the zero struct,
// so emptiness is inferred from the fields an image-backed volume always has.
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
// change has landed. Both are needed: after an extend or retype the volume is
// still at rest with its old value. A nil `settled` accepts any resting state.
func waitForVolume(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration, settled func(dtgo.GetVolumeDetails) bool) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.Volume.GetVolumeDetails(ctx, id, nil)
			if err != nil {
				// A volume that has not appeared yet is not a failure; create returns
				// before the platform has committed it.
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
			// Every other non-target status counts as pending, so an unfamiliar one is
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
		Delay:      wait.Pace(2 * time.Second),
		MinTimeout: wait.Pace(3 * time.Second),
		// Two readings in a row, so a poll landing between the action being accepted
		// and the volume leaving `available` cannot end the wait on its own.
		ContinuousTargetOccurence: 2,
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// waitForVolumeGone blocks until the volume stops resolving, so destroy does
// not return while the quota it occupies is still charged.
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
			// A failure state during deletion is terminal; waiting out the timeout
			// would only hide the reason.
			if volumeFailed(details.Status) {
				return nil, "", fmt.Errorf("volume %q entered %s state while being deleted", id, details.Status)
			}
			return "waiting", "waiting", nil
		},
		Timeout:    timeout,
		Delay:      wait.Pace(2 * time.Second),
		MinTimeout: wait.Pace(3 * time.Second),
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}
