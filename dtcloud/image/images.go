// Package image implements the dtcloud_image resource and its data sources.
//
// An image is created in two calls, not one. The create endpoint only opens an
// empty record — the image is `queued` and no machine can boot from it — and
// the disk file goes up in a separate upload request. A resource that stopped
// after create would leave a record nothing could ever use, so create here
// always uploads and always waits for `active`.
//
// Three API behaviours shape the rest of the code:
//
//   - The upload endpoint deletes the image if anything goes wrong, so a failed
//     create leaves nothing behind on the platform.
//   - Only four fields can be changed in place: name, os_distro, min_disk and
//     visibility, one request each. Everything else is ForceNew.
//   - Sizes are reported as formatted strings ("20 GB", "250 MB") and never as
//     numbers, so min_disk has to be parsed back out of one to be comparable.
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

// noHTMLPattern mirrors the character rule the API applies to an image name.
// Enforcing it in the schema turns a request rejected halfway through an apply
// into an error during plan.
const noHTMLPattern = `^[^<>&"']*$`

// visibilities are the four values the API accepts. Unlike os_distro and
// disk_format, this set is fixed in the code rather than read from the
// platform's configuration, so it is safe to check during plan.
var visibilities = []string{"public", "private", "shared", "community"}

// imageSettled reports whether a status means the platform has finished.
// `active` is the only usable resting state: an image in any other status
// either has no data yet or cannot be booted from.
func imageSettled(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "active")
}

// imageFailed reports whether a status means the image will never become
// active on its own.
//
// Only these four are terminal; every other status is treated as pending by
// the waiters below. Enumerating the transitional ones instead would mean an
// unfamiliar status failed the wait rather than being waited out.
//
//   - killed        the upload failed and the data was discarded
//   - deleted       the image was removed, which is what the API does to its
//     own record when an upload is rejected
//   - pending_delete
//   - deactivated   an administrator took it out of service
func imageFailed(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "killed", "deleted", "pending_delete", "deactivated":
		return true
	}
	return false
}

// createdImageID reads the new image's id out of a create response.
//
// The endpoint answers with the platform's raw image object rather than the
// wrapped shape the other services use, and nothing in the SDK is typed for it,
// so the body is parsed here. A resource that started life with an empty id is
// one Terraform would create a second time on the next apply.
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

// parseSizeGB turns the API's size strings back into whole GB.
//
// Every size on an image is reported as text: min_disk comes back as
// "20 GB" and as "-" when it was never set, and the payload size as "1.5 GB"
// or "250 MB". Anything that is not a whole number of GB — including a
// megabyte figure and the placeholder — reads as 0, which is what callers
// compare against when the platform has nothing to report.
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
// requested change has landed.
//
// The second condition is what makes this useful on an update: the four
// updatable fields are patched while the image sits at `active` throughout, so
// a wait that watched only the status would return before anything had
// changed. Callers pass the values they asked for.
//
// A nil `settled` means any resting state will do, which is what create wants —
// there the status really is the thing being waited on, since an image goes
// queued -> saving -> active as its data is written.
func waitForImage(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration, settled func(dtgo.GetImageDetails) bool) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.Image.GetImageDetails(ctx, id, nil)
			if err != nil {
				// An image that has not appeared yet is not a failure; create
				// can return before the platform has committed the record.
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
		// Two readings in a row, so a poll that lands in the gap between a
		// request being accepted and the image leaving its resting status
		// cannot end the wait on its own.
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
			// The platform reports a deletion in progress as pending_delete and
			// a finished one by dropping the image, so both count as done here.
			// imageFailed is not consulted: its statuses all mean "gone or
			// going", which during a delete is the outcome being waited for.
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
