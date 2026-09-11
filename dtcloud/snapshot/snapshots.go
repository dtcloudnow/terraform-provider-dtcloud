// Package snapshot implements the dtcloud_snapshot resource and its data
// sources.
//
// A snapshot is a point-in-time copy of a volume. It is taken against a volume
// and never moves, so volume_id is ForceNew; only name and description change
// in place.
//
// Two API behaviours shape the code here:
//
//   - The snapshot endpoints report the storage policy as a volume type id,
//     while the rest of the provider reports policy names. This package
//     resolves the id so both agree. See resolveStoragePolicy.
//   - `available` is the only resting state, and an update is not guaranteed to
//     have been applied by the time it is acknowledged. Waits are therefore on
//     the value that was requested, not on the status. See waitForSnapshot.
package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// noHTMLPattern mirrors the character rule the API applies to name and
// description. Enforcing it in the schema turns a rejected request halfway
// through an apply into an error during plan.
const noHTMLPattern = `^[^<>&"']*$`

// snapshotSettled reports whether a status means the platform has finished.
// `available` is the only resting state: unlike a volume, a snapshot is never
// reported as in use, even when a volume has been built from it.
func snapshotSettled(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "available")
}

// snapshotFailed reports whether a status means the platform gave up. Matched
// as a substring because the failure statuses are a family (`error`,
// `error_deleting`, ...) rather than a fixed set.
func snapshotFailed(status string) bool {
	return strings.Contains(strings.ToLower(status), "error")
}

// createdSnapshotID reads the new snapshot's id out of a create response.
// The typed value is used when present; the raw body is parsed as a fallback,
// because a resource that starts life with an empty id is one Terraform will
// create a second time on the next apply.
func createdSnapshotID(typed string, body string) (string, error) {
	if typed != "" {
		return typed, nil
	}

	var bare struct {
		ID         string `json:"id"`
		SnapshotID string `json:"snapshotId"`
		Snapshot   struct {
			ID string `json:"id"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(body), &bare); err != nil {
		return "", fmt.Errorf("the API response was not a JSON object: %w (body: %s)", err, body)
	}
	for _, candidate := range []string{bare.Snapshot.ID, bare.ID, bare.SnapshotID} {
		if candidate != "" {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no snapshot id in the API response (body: %s)", body)
}

// resolveStoragePolicy turns a volume type id into the policy name used
// everywhere else in the provider, so the same snapshot is not described by a
// UUID on one page and by a name on another.
//
// The lookup is memoised on the provider configuration and shared by every
// resource in a run. It is best-effort: if the storage policies cannot be read
// the name is left empty rather than failing a read that otherwise succeeded.
func resolveStoragePolicy(ctx context.Context, conf *config.CombinedConfig, volumeTypeID string) string {
	if volumeTypeID == "" {
		return ""
	}
	return conf.StoragePolicyNames(ctx)[volumeTypeID]
}

// waitForSnapshot blocks until the snapshot is at rest and `settled` agrees the
// requested change has landed.
//
// The second condition matters: an update is acknowledged while the snapshot is
// still reported as `available` with its old values, so a wait that only
// watched the status could return before anything had changed. Callers pass the
// value they asked for.
//
// A nil `settled` means any resting state will do, which is what create wants.
func waitForSnapshot(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration, settled func(dtgo.GetSnapshotDetails) bool) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.Snapshot.GetSnapshotDetails(ctx, id, nil)
			if err != nil {
				// A snapshot that has not appeared yet is not a failure; create
				// returns before the platform has committed it.
				if dterr.IsNotFound(err) {
					return "waiting", "waiting", nil
				}
				return nil, "", err
			}
			if details.Snapshot.ID == "" {
				return "waiting", "waiting", nil
			}
			if snapshotFailed(details.Snapshot.Status) {
				return nil, "", fmt.Errorf("snapshot %q entered %s state", id, details.Snapshot.Status)
			}
			// Every other non-target status counts as pending. The
			// transitional states are deliberately not enumerated, so an
			// unfamiliar one is waited out rather than treated as an error.
			if !snapshotSettled(details.Snapshot.Status) {
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
		// request being accepted and the snapshot leaving `available` cannot
		// end the wait on its own.
		ContinuousTargetOccurence: 2,
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// waitForSnapshotGone blocks until the snapshot stops resolving, so destroy
// does not return while the platform is still tearing it down and the quota it
// occupies is still charged.
func waitForSnapshotGone(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.Snapshot.GetSnapshotDetails(ctx, id, nil)
			if err != nil {
				if dterr.IsNotFound(err) {
					return "done", "done", nil
				}
				return nil, "", err
			}
			if details.Snapshot.ID == "" {
				return "done", "done", nil
			}
			// error_deleting is terminal: the snapshot will not disappear on
			// its own, and waiting the full timeout only hides why.
			if snapshotFailed(details.Snapshot.Status) {
				return nil, "", fmt.Errorf("snapshot %q entered %s state while being deleted", id, details.Snapshot.Status)
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

// snapshotAttributesSchema is the read-only half of a snapshot, shared by the
// resource and the singular data source so the two cannot drift apart.
func snapshotAttributesSchema() map[string]*schema.Schema {
	return map[string]*schema.Schema{
		"status": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "Status reported by the platform. `available` is the only resting state.",
		},
		"size": {
			Type:     schema.TypeInt,
			Computed: true,
			Description: "Size in GB, inherited from the source volume. A snapshot cannot be " +
				"resized; it is as large as the volume was when it was taken.",
		},
		"volume_type_id": {
			Type:     schema.TypeString,
			Computed: true,
			Description: "ID of the volume type backing the snapshot, as the snapshots endpoint " +
				"reports it. `storage_policy` is the same thing by name.",
		},
		"storage_policy": {
			Type:     schema.TypeString,
			Computed: true,
			Description: "Name of the storage policy backing the snapshot, resolved from " +
				"`volume_type_id`. Empty if the storage-policies endpoint could not be read.",
		},
		"progress": {
			Type:     schema.TypeString,
			Computed: true,
			Description: "How far the copy has got, as a percentage string such as `100%`. The " +
				"only visible sign that a large snapshot is still being written.",
		},
		"project_id": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "ID of the project the snapshot belongs to.",
		},
		"created_at": {
			Type:     schema.TypeString,
			Computed: true,
			Description: "When the snapshot was taken, as the platform reports it. The API sends " +
				"no timezone, and the value is passed through unchanged.",
		},
		"updated_at": {
			Type:     schema.TypeString,
			Computed: true,
			Description: "When the snapshot's metadata last changed. Empty until it is first " +
				"renamed or re-described.",
		},
	}
}

// setSnapshotAttributes writes the read-only half of a snapshot into state.
func setSnapshotAttributes(ctx context.Context, d *schema.ResourceData, conf *config.CombinedConfig, s dtgo.GetSnapshotDetails) {
	d.Set("status", s.Snapshot.Status)
	d.Set("size", s.Snapshot.Size)
	d.Set("volume_type_id", s.Snapshot.VolumeTypeID)
	d.Set("storage_policy", resolveStoragePolicy(ctx, conf, s.Snapshot.VolumeTypeID))
	d.Set("progress", s.Snapshot.OsExtendedSnapshotAttributesProgress)
	d.Set("project_id", s.Snapshot.OsExtendedSnapshotAttributesProjectID)
	d.Set("created_at", s.Snapshot.CreatedAt)
	d.Set("updated_at", s.Snapshot.UpdatedAt)
}
