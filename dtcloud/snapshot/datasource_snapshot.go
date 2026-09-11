package snapshot

import (
	"context"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// DataSourceDtcloudSnapshot looks up one snapshot by id, for snapshots this
// configuration does not own — as a resource, a later destroy would delete the
// copy it was there to protect.
func DataSourceDtcloudSnapshot() *schema.Resource {
	s := map[string]*schema.Schema{
		"id": {
			Type:         schema.TypeString,
			Required:     true,
			ValidateFunc: validation.NoZeroValues,
			Description:  "ID of the snapshot to look up.",
		},
		"name": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "Name of the snapshot.",
		},
		"volume_id": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "ID of the volume the snapshot was taken from.",
		},
		"description": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "Free-text description, as the platform reports it.",
		},
	}
	for name, attr := range snapshotAttributesSchema() {
		s[name] = attr
	}

	return &schema.Resource{
		Description: "Looks up one snapshot by id.\n\n" +
			"Use it to reference a snapshot Terraform did not take. Declaring such a snapshot as a " +
			"resource would hand Terraform ownership of it, and a later destroy would delete the copy it " +
			"was there to protect.",

		ReadContext: dataSourceDtcloudSnapshotRead,
		Schema:      s,
	}
}

func dataSourceDtcloudSnapshotRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	conf := meta.(*config.CombinedConfig)
	client := conf.DTClient()
	id := d.Get("id").(string)

	details, _, err := client.Snapshot.GetSnapshotDetails(ctx, id, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("Snapshot %q not found", id)
		}
		return diag.Errorf("Error retrieving snapshot %q: %s", id, err)
	}
	if details.Snapshot.ID == "" {
		return diag.Errorf("Snapshot %q not found", id)
	}

	d.SetId(details.Snapshot.ID)
	d.Set("name", details.Snapshot.Name)
	d.Set("volume_id", details.Snapshot.VolumeID)
	d.Set("description", details.Snapshot.Description)
	setSnapshotAttributes(ctx, d, conf, details)

	return nil
}
