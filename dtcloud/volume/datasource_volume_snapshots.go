package volume

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// DataSourceDtcloudVolumeSnapshots lists the snapshots taken of one volume.
//
// Read-only: snapshots have their own resource. This is for seeing what a
// destroy would take with it — `DELETE /volumes/{id}` is issued with
// `cascade: true`, so a volume's snapshots go with it.
func DataSourceDtcloudVolumeSnapshots() *schema.Resource {
	return &schema.Resource{
		Description: "Lists the snapshots taken of one volume.\n\n" +
			"Read-only on purpose: snapshots have their own service and their own routes, and creating " +
			"one here would put two resources in charge of the same object.\n\n" +
			"What this is for is seeing what would be lost. Deleting a volume cascades, so destroying one " +
			"destroys its snapshots too, and this is the way to check before running it.",

		ReadContext: dataSourceDtcloudVolumeSnapshotsRead,
		Schema: map[string]*schema.Schema{
			"volume_id": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the volume whose snapshots to list.",
			},
			"snapshots": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "Snapshots of the volume, newest first — the API sorts by creation date descending.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":             {Type: schema.TypeString, Computed: true},
						"name":           {Type: schema.TypeString, Computed: true},
						"status":         {Type: schema.TypeString, Computed: true},
						"description":    {Type: schema.TypeString, Computed: true},
						"size":           {Type: schema.TypeInt, Computed: true, Description: "Size in GB."},
						"storage_policy": {Type: schema.TypeString, Computed: true},
						"created_on":     {Type: schema.TypeString, Computed: true},
					},
				},
			},
		},
	}
}

func dataSourceDtcloudVolumeSnapshotsRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	volumeID := d.Get("volume_id").(string)

	snapshots, _, err := client.Volume.ListVolumeSnapshots(ctx, volumeID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("Volume %q not found", volumeID)
		}
		return diag.Errorf("Error listing snapshots of volume %q: %s", volumeID, err)
	}

	out := make([]interface{}, 0, len(snapshots))
	ids := make([]string, 0, len(snapshots))
	for _, s := range snapshots {
		out = append(out, map[string]interface{}{
			"id":             s.ID,
			"name":           s.Name,
			"status":         s.Status,
			"description":    s.Description,
			"size":           s.Size,
			"storage_policy": s.StoragePolicy,
			"created_on":     s.CreatedOn,
		})
		ids = append(ids, s.ID)
	}

	if err := d.Set("snapshots", out); err != nil {
		return diag.FromErr(err)
	}

	sum := sha256.Sum256([]byte(volumeID + "|" + strings.Join(ids, ",")))
	d.SetId(fmt.Sprintf("%x", sum[:8]))

	return nil
}
