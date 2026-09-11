// Package image implements the dtcloud_image resource and its data sources.
//
// An image takes two calls to create: one opens an empty `queued` record, the
// other uploads the file. Create does both and waits for `active`.
//
// Three API behaviours shape it: a failed upload deletes the image, so a failed
// create leaves nothing; only name, os_distro, min_disk and visibility change in
// place; and sizes are reported as strings, so min_disk has to be parsed.
package image

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// noHTMLPattern mirrors the character rule on an image name, so a request
// rejected halfway through an apply becomes an error during plan.
const noHTMLPattern = `^[^<>&"']*$`

// visibilities are the four values the API accepts. Fixed in code rather than
// read from the platform, so it is safe to check during plan.
var visibilities = []string{"public", "private", "shared", "community"}

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

// createdImageID reads the new image's id out of a create response, which is
// the raw image object rather than the wrapped shape the other services use.
func createdImageID(body string) (string, error) {
	var bare struct {
		ID      string `json:"id"`
		ImageID string `json:"imageId"`
		Image   struct {
			ID string `json:"id"`
		} `json:"image"`
	}
	if err := json.Unmarshal([]byte(body), &bare); err != nil {
		return "", fmt.Errorf("the API response was not a JSON object: %w (body: %s)", err, body)
	}
	for _, candidate := range []string{bare.ID, bare.ImageID, bare.Image.ID} {
		if candidate != "" {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no image id in the API response (body: %s)", body)
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
		Delay:      2 * time.Second,
		MinTimeout: 3 * time.Second,
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
		Delay:      2 * time.Second,
		MinTimeout: 3 * time.Second,
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
				"`killed` means the upload failed.",
		},
		"size": {
			Type:     schema.TypeString,
			Computed: true,
			Description: "Size of the uploaded data, as the platform formats it — `1.5 GB` or " +
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
