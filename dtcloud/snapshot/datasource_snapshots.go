package snapshot

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudSnapshots lists every snapshot visible to the caller — not
// the same as dtcloud_volume_snapshots, which is scoped to one volume and
// sorted newest-first. Filters are applied here. The storage policy is reported
// both as the volume type id and as the resolved name.
func DataSourceDtcloudSnapshots() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceDtcloudSnapshotsRead,
		Schema: map[string]*schema.Schema{
			"volume_id": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return snapshots of this volume. Applied by the provider.",
			},
			"name": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return snapshots with this exact name. Applied by the provider.",
			},
			"status": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return snapshots in this status, e.g. `available`. Applied by the provider.",
			},
			"snapshots": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The snapshots that matched, in the order the platform returned them.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":             {Type: schema.TypeString, Computed: true},
						"name":           {Type: schema.TypeString, Computed: true},
						"description":    {Type: schema.TypeString, Computed: true},
						"volume_id":      {Type: schema.TypeString, Computed: true},
						"status":         {Type: schema.TypeString, Computed: true},
						"size":           {Type: schema.TypeInt, Computed: true, Description: "Size in GB, inherited from the source volume."},
						"volume_type_id": {Type: schema.TypeString, Computed: true, Description: "ID of the volume type backing the snapshot."},
						"storage_policy": {Type: schema.TypeString, Computed: true, Description: "The same volume type by name, resolved by the provider."},
						"created_at":     {Type: schema.TypeString, Computed: true},
						"updated_at":     {Type: schema.TypeString, Computed: true, Description: "Empty until the snapshot is first renamed or re-described."},
					},
				},
			},
		},
	}
}

func dataSourceDtcloudSnapshotsRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	conf := meta.(*config.CombinedConfig)
	client := conf.DTClient()

	list, _, err := client.Snapshot.ListSnapshots(ctx, nil)
	if err != nil {
		return diag.Errorf("Error listing snapshots: %s", err)
	}

	volumeFilter := d.Get("volume_id").(string)
	nameFilter := d.Get("name").(string)
	statusFilter := d.Get("status").(string)

	// One lookup for the whole page. A failure leaves the column empty rather
	// than failing a listing that otherwise succeeded.
	policies := conf.StoragePolicyNames(ctx)

	out := make([]interface{}, 0, len(list.Snapshots))
	ids := make([]string, 0, len(list.Snapshots))
	for _, s := range list.Snapshots {
		if volumeFilter != "" && s.VolumeID != volumeFilter {
			continue
		}
		if nameFilter != "" && s.Name != nameFilter {
			continue
		}
		if statusFilter != "" && !strings.EqualFold(s.Status, statusFilter) {
			continue
		}
		out = append(out, map[string]interface{}{
			"id":             s.ID,
			"name":           s.Name,
			"description":    s.Description,
			"volume_id":      s.VolumeID,
			"status":         s.Status,
			"size":           s.Size,
			"volume_type_id": s.VolumeTypeID,
			"storage_policy": policies[s.VolumeTypeID],
			"created_at":     s.CreatedAt,
			"updated_at":     s.UpdatedAt,
		})
		ids = append(ids, s.ID)
	}

	if err := d.Set("snapshots", out); err != nil {
		return diag.FromErr(err)
	}

	// A stable id keeps an unchanged result from showing up as a diff.
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	d.SetId(fmt.Sprintf("%x", sum[:8]))

	return nil
}
