package volume

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudVolume manages a block storage volume.
//
// In place: `name`, `description`, `size` (grow only) and `storage_policy`.
// ForceNew: `image_id`, `source_volume_id` and `source_snapshot_id`, which seed
// the contents at create time and cannot re-seed an existing volume.
//
// Shrinking is refused rather than made ForceNew, which would destroy the data
// to satisfy the plan.
func ResourceDtcloudVolume() *schema.Resource {
	return &schema.Resource{
		Description: "Manages a block storage volume.",

		CreateContext: resourceDtcloudVolumeCreate,
		ReadContext:   resourceDtcloudVolumeRead,
		UpdateContext: resourceDtcloudVolumeUpdate,
		DeleteContext: resourceDtcloudVolumeDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
				ValidateFunc: validation.All(
					validation.NoZeroValues,
					validation.StringMatch(regexp.MustCompile(noHTMLPattern),
						`must not contain any of < > & " '`),
				),
				Description: "Name of the volume. Can be changed in place.",
			},
			"size": {
				Type:         schema.TypeInt,
				Required:     true,
				ValidateFunc: validation.IntBetween(minVolumeSizeGB, maxVolumeSizeGB),
				Description: "Size in GB, between 1 and 8192. Can be grown in place; " +
					"shrinking is impossible and is refused when applied.",
			},
			"storage_policy": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.NoZeroValues,
				Description: "Name of the storage policy backing the volume, as listed by the " +
					"dtcloud_storage_policies data source. Changing it retypes the volume in place.",
			},
			"description": {
				Type:     schema.TypeString,
				Optional: true,
				ValidateFunc: validation.StringMatch(regexp.MustCompile(noHTMLPattern),
					`must not contain any of < > & " '`),
				Description: "Free-text description. Write-only: the API accepts it but never " +
					"reports it back, so it does not drift, does not survive import, and cannot be cleared.",
			},

			// The three create-time sources for the volume's contents: exactly one of
			// them, or none for a blank volume. Each goes to a different endpoint.
			"image_id": {
				Type:          schema.TypeString,
				Optional:      true,
				ForceNew:      true,
				ConflictsWith: []string{"source_volume_id", "source_snapshot_id"},
				ValidateFunc:  validation.NoZeroValues,
				Description:   "Create the volume from this image, making it bootable. Changing it recreates the volume.",
			},
			"source_volume_id": {
				Type:          schema.TypeString,
				Optional:      true,
				ForceNew:      true,
				ConflictsWith: []string{"image_id", "source_snapshot_id"},
				ValidateFunc:  validation.NoZeroValues,
				Description:   "Create the volume as a clone of this one. Changing it recreates the volume.",
			},
			"source_snapshot_id": {
				Type:          schema.TypeString,
				Optional:      true,
				ForceNew:      true,
				ConflictsWith: []string{"image_id", "source_volume_id"},
				ValidateFunc:  validation.NoZeroValues,
				Description: "Restore the volume from this snapshot. `size` must be at least the " +
					"snapshot's size. Changing it recreates the volume.",
			},

			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Status reported by the platform, e.g. available or in-use.",
			},
			"bootable": {
				Type:        schema.TypeBool,
				Computed:    true,
				Description: "Whether the volume can be booted from. True for volumes built from an image.",
			},
			"volume_type": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "HDD, or CD-ROM when the volume holds an ISO. Derived by the platform, not the storage policy.",
			},
			"attached_to": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Name of the VM this volume is attached to, empty when detached.",
			},
			"attached_to_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "ID of the VM this volume is attached to, empty when detached.",
			},
			"attached_to_status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Status of the VM this volume is attached to.",
			},
			"is_detachable": {
				Type:        schema.TypeBool,
				Computed:    true,
				Description: "Whether the platform will allow this volume to be detached.",
			},
			"created": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "When the volume was created, as reported by the platform.",
			},
			"last_modified": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "When the volume last changed, as reported by the platform.",
			},
			"image_metadata": imageMetadataSchema(),
		},

		// The grow-only rule lives here rather than in CustomizeDiff: at plan time a
		// lowered number and a volume grown outside Terraform are the same diff, and
		// refusing there would also block the destroy plan.

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(30 * time.Minute),
			Update: schema.DefaultTimeout(30 * time.Minute),
			Delete: schema.DefaultTimeout(20 * time.Minute),
		},
	}
}

// createdVolumeID reads the new volume's id out of a create, clone or restore
// response. The three answer with different shapes:
//
//	create   ->  a built response, keyed `volumeId`
//	clone    ->  the raw volume object, keyed `id`
//	restore  ->  the raw volume object, keyed `id`
//
// The typed value is used when present, the raw body as a fallback.
func createdVolumeID(typed string, body string) (string, error) {
	if typed != "" {
		return typed, nil
	}

	var bare struct {
		VolumeID string `json:"volumeId"`
		ID       string `json:"id"`
		Volume   struct {
			ID string `json:"id"`
		} `json:"volume"`
	}
	if err := json.Unmarshal([]byte(body), &bare); err != nil {
		return "", fmt.Errorf("the API response was not a JSON object: %w (body: %s)", err, body)
	}
	for _, candidate := range []string{bare.VolumeID, bare.ID, bare.Volume.ID} {
		if candidate != "" {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no volume id in the API response (body: %s)", body)
}

func resourceDtcloudVolumeCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	params := dtgo.CreateVolumeParams{
		Name:          d.Get("name").(string),
		StoragePolicy: d.Get("storage_policy").(string),
		Size:          d.Get("size").(int),
	}

	var (
		typedID string
		body    string
		err     error
	)

	if source := d.Get("source_snapshot_id").(string); source != "" {
		// Restoring goes through the snapshot service, which answers with the raw
		// body only — what createdVolumeID's fallback parser is for.
		restore := dtgo.CreateVolumeFromSnapshotParams{
			Name:          params.Name,
			Size:          params.Size,
			StoragePolicy: params.StoragePolicy,
		}
		body, err = client.Snapshot.CreateVolumeFromSnapshot(ctx, source, restore, nil)
		if err != nil {
			return diag.Errorf("Error creating volume from snapshot %q: %s", source, err)
		}
	} else if source := d.Get("source_volume_id").(string); source != "" {
		// Clone takes name, size and storage policy only.
		var resp dtgo.UpdateVolume
		resp, body, err = client.Volume.CloneVolume(ctx, source, params, nil)
		if err != nil {
			return diag.Errorf("Error cloning volume %q: %s", source, err)
		}
		typedID = resp.ID
	} else {
		params.ImageRef = d.Get("image_id").(string)
		var resp dtgo.CreateVolume
		resp, body, err = client.Volume.CreateVolume(ctx, params, nil)
		if err != nil {
			return diag.Errorf("Error creating volume: %s", err)
		}
		typedID = resp.VolumeID
	}

	id, err := createdVolumeID(typedID, body)
	if err != nil {
		return diag.Errorf("Error reading the created volume's id: %s", err)
	}
	d.SetId(id)

	if err := waitForVolume(ctx, client, id, d.Timeout(schema.TimeoutCreate), nil); err != nil {
		return diag.Errorf("Error waiting for volume %q to become available: %s", id, err)
	}

	// Create accepts no description, so one is applied by a follow-up update.
	if description := d.Get("description").(string); description != "" {
		update := dtgo.UpdateVolumeParams{Name: params.Name, Description: description}
		if _, _, err := client.Volume.UpdateVolume(ctx, id, update, nil); err != nil {
			return diag.Errorf("Error setting the description on volume %q: %s", id, err)
		}
	}

	return resourceDtcloudVolumeRead(ctx, d, meta)
}

func resourceDtcloudVolumeRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	details, _, err := client.Volume.GetVolumeDetails(ctx, d.Id(), nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error retrieving volume %q: %s", d.Id(), err)
	}
	if details.ID == "" {
		d.SetId("")
		return nil
	}

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

	// `description` is not set: the details endpoint reports none. Nor are the three
	// sources — image_metadata.image_id is inherited by a clone or a restore, so
	// writing it into image_id would propose a replacement.

	return nil
}

func resourceDtcloudVolumeUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	id := d.Id()

	if d.HasChanges("name", "description") {
		// Both are sent every time, so the request does not depend on which one
		// Terraform happened to notice had changed.
		params := dtgo.UpdateVolumeParams{
			Name:        d.Get("name").(string),
			Description: d.Get("description").(string),
		}
		if _, _, err := client.Volume.UpdateVolume(ctx, id, params, nil); err != nil {
			return diag.Errorf("Error updating volume %q: %s", id, err)
		}
	}

	if d.HasChange("size") {
		oldRaw, newRaw := d.GetChange("size")
		oldSize, newSize := oldRaw.(int), newRaw.(int)

		// Grow-only. See the resource comment for why this is not a CustomizeDiff.
		if newSize < oldSize {
			return diag.Errorf(
				"size cannot be reduced from %d to %d: volumes can only be grown. "+
					"Shrinking would mean destroying and recreating the volume, and its data with it. "+
					"If the volume was grown outside Terraform, raise size to %d to match it",
				oldSize, newSize, oldSize)
		}

		action := dtgo.PerformActionParams{OsExtend: &dtgo.OsExtend{NewSize: newSize}}
		if _, err := client.Volume.PerformActionOnVolume(ctx, id, action, nil); err != nil {
			return diag.Errorf("Error extending volume %q to %d GB: %s", id, newSize, err)
		}

		// Waiting on the size, not the status: the volume is already at rest when
		// the extend is accepted and stays that way for a while.
		settled := func(v dtgo.GetVolumeDetails) bool { return v.Size >= newSize }
		if err := waitForVolume(ctx, client, id, d.Timeout(schema.TimeoutUpdate), settled); err != nil {
			return diag.Errorf("Error waiting for volume %q to reach %d GB: %s", id, newSize, err)
		}
	}

	if d.HasChange("storage_policy") {
		policy := d.Get("storage_policy").(string)
		action := dtgo.PerformActionParams{OsRetype: &dtgo.OsRetype{NewType: policy}}
		if _, err := client.Volume.PerformActionOnVolume(ctx, id, action, nil); err != nil {
			return diag.Errorf("Error retyping volume %q to %q: %s", id, policy, err)
		}

		// Same as the extend: the policy is what changes, so it is what is waited on.
		settled := func(v dtgo.GetVolumeDetails) bool {
			return strings.EqualFold(v.StoragePolicy, policy)
		}
		if err := waitForVolume(ctx, client, id, d.Timeout(schema.TimeoutUpdate), settled); err != nil {
			return diag.Errorf("Error waiting for volume %q to be retyped to %q: %s", id, policy, err)
		}
	}

	return resourceDtcloudVolumeRead(ctx, d, meta)
}

func resourceDtcloudVolumeDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	// The platform refuses to delete an `in-use` volume, and its refusal lists
	// several conditions without saying which applied, so reading the volume first
	// turns that into a message naming the machine. Detaching is not done here:
	// this resource does not own the attachment.
	details, _, err := client.Volume.GetVolumeDetails(ctx, d.Id(), nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error retrieving volume %q before deleting it: %s", d.Id(), err)
	}
	if strings.EqualFold(strings.TrimSpace(details.Status), "in-use") {
		return diag.Errorf(
			"Volume %q is still attached to %s and cannot be deleted; detach it first.\n\n"+
				"The platform refuses to delete a volume that is in use, and this provider will "+
				"not detach one on your behalf — disconnecting a disk from a running machine is "+
				"not something a destroy should do unasked.\n\n"+
				"If the attachment is managed by dtcloud_vm_volume_attachment, check that it "+
				"references this volume so Terraform removes it first. If it was attached outside "+
				"Terraform, detach it there before destroying.",
			d.Id(), attachedToDescription(details))
	}

	// The route deletes with cascade, so this takes the volume's snapshots with
	// it. Documented on the resource page.
	if _, err := client.Volume.DeleteVolume(ctx, d.Id(), nil); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error deleting volume %q: %s", d.Id(), err)
		}
	}

	if err := waitForVolumeGone(ctx, client, d.Id(), d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.Errorf("Error waiting for volume %q to be deleted: %s", d.Id(), err)
	}

	d.SetId("")
	return nil
}
