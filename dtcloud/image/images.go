// Package image implements the dtcloud_image resource and its data sources.
//
// An image is made by capturing a volume, not by uploading a file: the upload
// endpoint answers 403 for customer accounts, so a volume is the only source.
//
// Four API behaviours shape it: the capture answers 200 with an empty body, so
// the new id is found by diffing the image list; the volume must be `available`
// and is held for the duration; only name, os_distro, min_disk and visibility
// change in place; and sizes are reported as strings.
package image

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

// noHTMLPattern mirrors the character rule on an image name, so a request
// rejected halfway through an apply becomes an error during plan.
const noHTMLPattern = `^[^<>&"']*$`

// imageSettled reports whether a status means the platform has finished.
// `active` is the only usable resting state.
func imageSettled(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "active")
}

// imageFailed reports whether a status is terminal. Every other status counts
// as pending, so an unfamiliar one is waited out rather than failed.
func imageFailed(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "killed", "deleted", "pending_delete", "deactivated":
		return true
	}
	return false
}

// imageIDs is the set of image ids visible right now, used to tell which image
// a capture produced.
func imageIDs(ctx context.Context, client *dtgo.Client) (map[string]bool, error) {
	list, _, err := client.Image.ListImages(ctx, nil)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(list))
	for _, img := range list {
		ids[img.ID] = true
	}
	return ids, nil
}

// findCapturedImage identifies the image a volume capture just made. The action
// reports nothing about it, so the only way to name it is to look for an id that
// was not there before. Two matches is an error rather than a guess.
func findCapturedImage(ctx context.Context, client *dtgo.Client, name string, before map[string]bool, timeout time.Duration) (string, error) {
	var found string

	err := retry.RetryContext(ctx, timeout, func() *retry.RetryError {
		list, _, err := client.Image.ListImages(ctx, nil)
		if err != nil {
			return retry.NonRetryableError(err)
		}

		var fresh []string
		for _, img := range list {
			if !before[img.ID] && img.Name == name {
				fresh = append(fresh, img.ID)
			}
		}

		switch len(fresh) {
		case 0:
			// The record can take a moment to show up in the listing.
			return retry.RetryableError(fmt.Errorf("no new image named %q yet", name))
		case 1:
			found = fresh[0]
			return nil
		default:
			return retry.NonRetryableError(fmt.Errorf(
				"%d new images are named %q (%s); the capture cannot be told apart from "+
					"whatever else created one, so none is adopted",
				len(fresh), name, strings.Join(fresh, ", ")))
		}
	})
	if err != nil {
		return "", err
	}
	return found, nil
}

// waitForVolumeAvailable blocks until the volume can be captured. One busy with
// another capture is refused outright rather than queued.
func waitForVolumeAvailable(ctx context.Context, client *dtgo.Client, volumeID string, timeout time.Duration) error {
	return retry.RetryContext(ctx, timeout, func() *retry.RetryError {
		details, _, err := client.Volume.GetVolumeDetails(ctx, volumeID, nil)
		if err != nil {
			return retry.NonRetryableError(err)
		}
		switch details.Status {
		case "available":
			return nil
		case "error", "error_deleting":
			return retry.NonRetryableError(fmt.Errorf(
				"volume %q is in status %q", volumeID, details.Status))
		default:
			return retry.RetryableError(fmt.Errorf(
				"volume %q is %q, not yet available", volumeID, details.Status))
		}
	})
}

// parseSizeGB turns the API's size strings back into whole GB. Anything that is
// not a whole number of GB — "1.5 GB", "250 MB", "-" — reads as 0.
func parseSizeGB(value string) int {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 2 || !strings.EqualFold(fields[1], "GB") {
		return 0
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0
	}
	return n
}

// waitForImage blocks until the image is at rest and `settled` agrees the
// change has landed — both are needed on an update, where the image stays
// `active` throughout. A nil `settled` accepts any resting state, for create.
func waitForImage(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration, settled func(dtgo.GetImageDetails) bool) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.Image.GetImageDetails(ctx, id, nil)
			if err != nil {
				// An image that has not appeared yet is not a failure; create can return
				// before the platform has committed the record.
				if dterr.IsNotFound(err) {
					return "waiting", "waiting", nil
				}
				return nil, "", err
			}
			if details.ID == "" {
				return "waiting", "waiting", nil
			}
			if imageFailed(details.Status) {
				return nil, "", fmt.Errorf("image %q entered %s state", id, details.Status)
			}
			if !imageSettled(details.Status) {
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
		// Two readings in a row, so a poll that lands between a request being
		// accepted and the image leaving its resting status cannot end the wait.
		ContinuousTargetOccurence: 2,
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// waitForImageGone blocks until the image stops resolving, so destroy does not
// return while the platform is still reclaiming the space it occupied.
func waitForImageGone(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.Image.GetImageDetails(ctx, id, nil)
			if err != nil {
				if dterr.IsNotFound(err) {
					return "done", "done", nil
				}
				return nil, "", err
			}
			if details.ID == "" {
				return "done", "done", nil
			}
			// A deletion in progress reports pending_delete and a finished one drops
			// the image, so both count as done.
			if strings.EqualFold(details.Status, "deleted") || strings.EqualFold(details.Status, "pending_delete") {
				return "done", "done", nil
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

// imageAttributesSchema is the read-only half of an image, shared by the
// resource and the singular data source so the two cannot drift apart.
func imageAttributesSchema() map[string]*schema.Schema {
	return map[string]*schema.Schema{
		"status": {
			Type:     schema.TypeString,
			Computed: true,
			Description: "Status reported by the platform. `active` is the only status a machine " +
				"can be built from; `queued` and `saving` mean the data is not there yet, and " +
				"`killed` means the capture failed.",
		},
		"size": {
			Type:     schema.TypeString,
			Computed: true,
			Description: "Size of the captured data, as the platform formats it — `1.2 GB` or " +
				"`250 MB`. There is no endpoint that reports it as a number. An image with no " +
				"data yet reads as `0 MB`.",
		},
		"type": {
			Type:     schema.TypeString,
			Computed: true,
			Description: "How the platform categorises the image: `ISO` or `Template (VM)`. This " +
				"is not `disk_format` — it is derived from it, and the two are not interchangeable.",
		},
		"os_type": {
			Type:     schema.TypeString,
			Computed: true,
			Description: "`linux` or `windows`, when the platform reports it. Frequently empty " +
				"here: the details endpoint passes the value straight through, while the list " +
				"endpoint behind `dtcloud_images` guesses it from `os_distro` when it is missing.",
		},
	}
}

// setImageAttributes writes the read-only half of an image into state.
func setImageAttributes(d *schema.ResourceData, img dtgo.GetImageDetails) {
	d.Set("status", img.Status)
	d.Set("size", img.Size)
	d.Set("type", img.Type)
	d.Set("os_type", img.OsType)
}
