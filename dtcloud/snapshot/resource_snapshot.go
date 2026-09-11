package snapshot

import (
	"context"
	"regexp"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudSnapshot manages a point-in-time copy of a volume. In place:
// name and description. Size and storage policy come from the source volume, so
// volume_id is ForceNew; the source may be attached to a running machine.
// Restoring produces a volume and belongs to dtcloud_volume.source_snapshot_id.
func ResourceDtcloudSnapshot() *schema.Resource {
	s := map[string]*schema.Schema{
		"name": {
			Type:     schema.TypeString,
			Required: true,
			ValidateFunc: validation.All(
				validation.NoZeroValues,
				validation.StringMatch(regexp.MustCompile(noHTMLPattern),
					`must not contain any of < > & " '`),
			),
			Description: "Name of the snapshot. Can be changed in place.",
		},
		"volume_id": {
			Type:         schema.TypeString,
			Required:     true,
			ForceNew:     true,
			ValidateFunc: validation.NoZeroValues,
			Description: "ID of the volume to snapshot. The volume may be attached to a running " +
				"machine. Changing it takes a new snapshot and destroys this one.",
		},
		"description": {
			Type:     schema.TypeString,
			Optional: true,
			// Deliberately no NoZeroValues: omitting the argument and setting it to
			// "" both mean "no description", so neither may be an error.
			ValidateFunc: validation.StringMatch(regexp.MustCompile(noHTMLPattern),
				`must not contain any of < > & " '`),
			Description: "Free-text description. Removing it from the configuration clears it " +
				"on the platform — the API expresses that as a JSON null, since it rejects an " +
				"empty string outright.",
		},
	}
	for name, attr := range snapshotAttributesSchema() {
		s[name] = attr
	}

	return &schema.Resource{
		CreateContext: resourceDtcloudSnapshotCreate,
		ReadContext:   resourceDtcloudSnapshotRead,
		UpdateContext: resourceDtcloudSnapshotUpdate,
		DeleteContext: resourceDtcloudSnapshotDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		Schema: s,

		Timeouts: &schema.ResourceTimeout{
			// Creating copies the whole volume, so the default is generous.
			Create: schema.DefaultTimeout(30 * time.Minute),
			Update: schema.DefaultTimeout(15 * time.Minute),
			Delete: schema.DefaultTimeout(20 * time.Minute),
		},
	}
}

func resourceDtcloudSnapshotCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	params := dtgo.CreateSnapshotParams{
		Name:     d.Get("name").(string),
		VolumeID: d.Get("volume_id").(string),
	}

	resp, body, err := client.Snapshot.CreateSnapshot(ctx, params, nil)
	if err != nil {
		return diag.Errorf("Error creating snapshot of volume %q: %s", params.VolumeID, err)
	}

	id, err := createdSnapshotID(resp.Snapshot.ID, body)
	if err != nil {
		return diag.Errorf("Error reading the created snapshot's id: %s", err)
	}
	d.SetId(id)

	if err := waitForSnapshot(ctx, client, id, d.Timeout(schema.TimeoutCreate), nil); err != nil {
		return diag.Errorf("Error waiting for snapshot %q to become available: %s", id, err)
	}

	// Create accepts only a name and a volume id, so a description has to be
	// applied by a follow-up update.
	if description := d.Get("description").(string); description != "" {
		update := dtgo.UpdateSnapshotParams{Name: params.Name, Description: description}
		if _, _, err := client.Snapshot.UpdateSnapshot(ctx, id, update, nil); err != nil {
			return diag.Errorf("Error setting the description on snapshot %q: %s", id, err)
		}
		settled := func(s dtgo.GetSnapshotDetails) bool {
			return s.Snapshot.Description == description
		}
		if err := waitForSnapshot(ctx, client, id, d.Timeout(schema.TimeoutCreate), settled); err != nil {
			return diag.Errorf("Error waiting for the description on snapshot %q to be applied: %s", id, err)
		}
	}

	return resourceDtcloudSnapshotRead(ctx, d, meta)
}

func resourceDtcloudSnapshotRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	conf := meta.(*config.CombinedConfig)
	client := conf.DTClient()

	details, _, err := client.Snapshot.GetSnapshotDetails(ctx, d.Id(), nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error retrieving snapshot %q: %s", d.Id(), err)
	}
	if details.Snapshot.ID == "" {
		d.SetId("")
		return nil
	}

	d.Set("name", details.Snapshot.Name)
	d.Set("volume_id", details.Snapshot.VolumeID)

	// Unlike a volume's, a snapshot's description is reported back, so it drifts
	// like any other argument and survives an import.
	d.Set("description", details.Snapshot.Description)

	setSnapshotAttributes(ctx, d, conf, details)

	return nil
}

func resourceDtcloudSnapshotUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	if !d.HasChanges("name", "description") {
		return resourceDtcloudSnapshotRead(ctx, d, meta)
	}

	name := d.Get("name").(string)
	description := d.Get("description").(string)

	// An empty description means "remove it", expressed as a JSON null since an
	// empty string is rejected.

	// Both fields go on every update, so the request does not depend on which one
	// Terraform happened to notice had changed.
	params := dtgo.UpdateSnapshotParams{
		Name:             name,
		Description:      description,
		ClearDescription: description == "",
	}
	if _, _, err := client.Snapshot.UpdateSnapshot(ctx, d.Id(), params, nil); err != nil {
		return diag.Errorf("Error updating snapshot %q: %s", d.Id(), err)
	}

	// Wait on the requested values rather than the status: a snapshot never leaves
	// `available` and still carries the old values.
	settled := func(s dtgo.GetSnapshotDetails) bool {
		return s.Snapshot.Name == name && s.Snapshot.Description == description
	}
	if err := waitForSnapshot(ctx, client, d.Id(), d.Timeout(schema.TimeoutUpdate), settled); err != nil {
		return diag.Errorf("Error waiting for snapshot %q to be updated: %s", d.Id(), err)
	}

	return resourceDtcloudSnapshotRead(ctx, d, meta)
}

func resourceDtcloudSnapshotDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	if _, err := client.Snapshot.DeleteSnapshot(ctx, d.Id(), nil); err != nil {
		// Already gone is a successful destroy, not a failure.
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		// The platform refuses to delete a snapshot a volume still depends on, or
		// one still being created. Its message names the reason, so it is passed on.
		return diag.Errorf("Error deleting snapshot %q: %s", d.Id(), err)
	}

	if err := waitForSnapshotGone(ctx, client, d.Id(), d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.Errorf("Error waiting for snapshot %q to be deleted: %s", d.Id(), err)
	}

	d.SetId("")
	return nil
}
