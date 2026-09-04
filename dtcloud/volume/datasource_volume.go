package volume

import (
	"context"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// DataSourceDtcloudVolume looks up one volume by id.
//
// Use it to reference a volume Terraform did not create — attaching an existing
// disk to a new VM, for instance. Declaring it as a resource instead would hand
// Terraform ownership of it, and a later `terraform destroy` would delete the
// volume and its snapshots along with everything else.
func DataSourceDtcloudVolume() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceDtcloudVolumeRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the volume to look up.",
			},
			"name":               {Type: schema.TypeString, Computed: true, Description: "Name of the volume."},
			"size":               {Type: schema.TypeInt, Computed: true, Description: "Size in GB."},
			"storage_policy":     {Type: schema.TypeString, Computed: true, Description: "Storage policy backing the volume."},
			"status":             {Type: schema.TypeString, Computed: true, Description: "Status reported by the platform."},
			"bootable":           {Type: schema.TypeBool, Computed: true, Description: "Whether the volume can be booted from."},
			"volume_type":        {Type: schema.TypeString, Computed: true, Description: "HDD, or CD-ROM when the volume holds an ISO."},
			"attached_to":        {Type: schema.TypeString, Computed: true, Description: "Name of the VM this volume is attached to."},
			"attached_to_id":     {Type: schema.TypeString, Computed: true, Description: "ID of the VM this volume is attached to."},
			"attached_to_status": {Type: schema.TypeString, Computed: true, Description: "Status of the VM this volume is attached to."},
			"is_detachable":      {Type: schema.TypeBool, Computed: true, Description: "Whether the platform will allow this volume to be detached."},
			"created":            {Type: schema.TypeString, Computed: true, Description: "When the volume was created."},
			"last_modified":      {Type: schema.TypeString, Computed: true, Description: "When the volume last changed."},
			"image_metadata":     imageMetadataSchema(),

			// No `description`: the details endpoint does not report one. See
			// the note in resourceDtcloudVolumeRead.
		},
	}
}

func dataSourceDtcloudVolumeRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	id := d.Get("id").(string)

	details, _, err := client.Volume.GetVolumeDetails(ctx, id, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("Volume %q not found", id)
		}
		return diag.Errorf("Error retrieving volume %q: %s", id, err)
	}
	if details.ID == "" {
		return diag.Errorf("Volume %q not found", id)
	}

	d.SetId(details.ID)
	d.Set("name", details.Name)
	d.Set("size", details.Size)
	d.Set("storage_policy", details.StoragePolicy)
	d.Set("status", details.Status)
	d.Set("bootable", parseBootable(details.Bootable))
	d.Set("volume_type", details.Type)
	d.Set("attached_to", details.AttachedTo)
	d.Set("attached_to_id", details.AttachedToID)
	d.Set("attached_to_status", details.AttachedToStatus)
	d.Set("is_detachable", details.IsDetachable)
	d.Set("created", details.Created)
	d.Set("last_modified", details.LastModified)
	d.Set("image_metadata", flattenImageMetadata(details.VolumeImageMetadata))

	return nil
}
