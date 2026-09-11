// Package snapshot implements the dtcloud_snapshot resource and its data
// sources.
//
// A snapshot is a point-in-time copy of a volume. It never moves, so volume_id
// is ForceNew; only name and description change in place.
//
// Two API behaviours shape it: the storage policy is reported as a volume type
// id where the rest of the provider uses names, and an update is acknowledged
// before it is applied, so waits compare the value rather than the status.
package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// noHTMLPattern mirrors the character rule on name and description, so a
// request rejected halfway through an apply becomes an error during plan.
const noHTMLPattern = `^[^<>&"']*$`

// snapshotSettled reports whether a status means the platform has finished.
// `available` is the only resting state: a snapshot is never reported in use.
func snapshotSettled(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "available")
}

// snapshotFailed reports whether a status means the platform gave up. Matched
// as a substring: the failure statuses are a family, not a fixed set.
func snapshotFailed(status string) bool {
	return strings.Contains(strings.ToLower(status), "error")
}

// createdSnapshotID reads the new snapshot's id out of a create response. The
// typed value is used when present, the raw body as a fallback.
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
// elsewhere, cached for the run. Best-effort: an unreadable list leaves it empty.
func resolveStoragePolicy(ctx context.Context, conf *config.CombinedConfig, volumeTypeID string) string {
	if volumeTypeID == "" {
		return ""
	}
	return conf.StoragePolicyNames(ctx)[volumeTypeID]
}

// waitForSnapshot blocks until the snapshot is at rest and `settled` agrees the
// change has landed — an update is acknowledged while the old values still show.
// A nil `settled` accepts any resting state, for create.
func waitForSnapshot(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration, settled func(dtgo.GetSnapshotDetails) bool) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.Snapshot.GetSnapshotDetails(ctx, id, nil)
			if err != nil {
				// A snapshot that has not appeared yet is not a failure; create returns
				// before the platform has committed it.
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
			// Every other non-target status counts as pending, so an unfamiliar one
			// is waited out rather than treated as an error.
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
		// Two readings in a row, so a poll that lands between the request being
		// accepted and the snapshot leaving `available` cannot end the wait.
		ContinuousTargetOccurence: 2,
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// waitForSnapshotGone blocks until the snapshot stops resolving, so destroy
// does not return while the quota it occupies is still charged.
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
			// error_deleting is terminal: the snapshot will not disappear on its own.
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
