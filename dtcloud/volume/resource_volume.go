package volume

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudVolume manages a block storage volume.
//
// What can change in place: `name` and `description` (POST on the volume),
// `size` (osExtend) and `storage_policy` (osRetype).
// ForceNew: `image_id` and `source_volume_id` — both are create-time sources
// for the volume's contents and there is no endpoint that re-seeds an existing
// volume from either.
//
// `size` is grow-only. Neither OpenStack nor the API can shrink a volume, and
// making that a ForceNew would quietly destroy the data to satisfy the plan, so
// it is rejected during plan instead.
func ResourceDtcloudVolume() *schema.Resource {
	return &schema.Resource{
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

			// The two create-time sources for the volume's contents. Exactly one
			// of them, or neither for a blank volume.
			"image_id": {
				Type:          schema.TypeString,
				Optional:      true,
				ForceNew:      true,
				ConflictsWith: []string{"source_volume_id"},
				ValidateFunc:  validation.NoZeroValues,
				Description:   "Create the volume from this image, making it bootable. Changing it recreates the volume.",
			},
			"source_volume_id": {
				Type:          schema.TypeString,
				Optional:      true,
				ForceNew:      true,
				ConflictsWith: []string{"image_id"},
				ValidateFunc:  validation.NoZeroValues,
				Description:   "Create the volume as a clone of this one. Changing it recreates the volume.",
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

		// The grow-only rule is enforced in Update, not here. That is a
		// deliberate move away from this package's usual preference for
		// plan-time errors, and it cost a live debugging session to learn why.
		//
		// A CustomizeDiff cannot tell the two shrink-shaped situations apart:
		//
		//   the user lowered the number    state 40, config 20
		//   reality grew outside Terraform state 40, config 20
		//
		// They are identical from inside the callback. So a rule that rejects
		// the first also rejects the second — and the second includes the plan
		// Terraform builds while refreshing for a destroy. A volume somebody
		// grew in the web console became impossible to destroy: every plan was
		// refused, and the only way out was editing the configuration to a size
		// nobody had chosen. Confirmed live on DEV, and reproduced by
		// TestAccDtcloudVolume_shrinkRuleDoesNotBlockDestroy.
		//
		// Update never runs during a destroy, so putting the check there keeps
		// the refusal and gives the resource back its escape hatch.

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(30 * time.Minute),
			Update: schema.DefaultTimeout(30 * time.Minute),
			Delete: schema.DefaultTimeout(20 * time.Minute),
		},
	}
}

// createdVolumeID digs the new volume's id out of a create or clone response.
//
// The two paths answer differently, because the route builds one response and
// passes the other straight through from OpenStack:
//
//	POST /volumes        ->  the client-panel object, keyed `volumeId`
//	POST /{id}/clone     ->  the raw OpenStack volume, keyed `id`
//
// dt-go types each one, so `typed` is usually filled in. The raw body is parsed
// as a fallback for the case the typed decode came back empty — which is how
// the equivalent asymmetry on networks was found.
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

	if source := d.Get("source_volume_id").(string); source != "" {
		// Clone takes name, size and storagePolicy only — imageRef is not part
		// of cloneVolumeSchema, and ConflictsWith has already ruled it out.
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

	// The create call has no description parameter — createVolumeSchema does not
	// accept one — so a description is applied as a follow-up update.
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

	// `description` is deliberately not set. getVolumeDetailsForClient builds
	// its response field by field and leaves the description out, so there is
	// nothing to read it from — setting it here from anything would be
	// inventing a value. `image_id` and `source_volume_id` are likewise not
	// recovered: neither is reported, and image_metadata.image_id is the id of
	// the image the *contents* came from, which a cloned volume inherits from
	// its source. Writing that into image_id would propose a replacement on the
	// next plan.

	return nil
}

func resourceDtcloudVolumeUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	id := d.Id()

	if d.HasChanges("name", "description") {
		// Both are sent every time. The endpoint treats them independently and
		// dt-go marks each omitempty, so sending the current name alongside a
		// changed description costs nothing and keeps the two in step.
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

		// Grow-only. See the note on the resource for why this lives here and
		// not in a CustomizeDiff: the plan-time version also blocked destroys.
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

		// Waiting on the size, not the status. The volume is already
		// `available` when the extend is accepted and stays that way for a
		// while, so a status-only wait would return having done nothing — the
		// bug that shipped once on VM resize.
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

		// Same reasoning as the extend above: the policy is what changes, so
		// the policy is what is waited on.
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

	// An attached volume cannot be deleted. Not slowly, not eventually — the
	// platform refuses outright while the status is `in-use`, and the web
	// console enforces the same rule. Reading the volume first turns that into
	// a message naming the machine, instead of Cinder's answer arriving
	// mid-destroy:
	//
	//   Invalid volume: Volume  must not be migrating, attached, belong to a
	//   group, have snapshots or be disassociated from snapshots after volume
	//   transfer.
	//
	// which lists five conditions without saying which one applied. Observed
	// live on DEV.
	//
	// Detaching here is deliberately not done. This resource does not own the
	// attachment, and pulling a disk out of a running machine to make a destroy
	// succeed is the same class of silent damage as shrinking a volume to
	// satisfy a plan. When the attachment is managed by
	// dtcloud_vm_volume_attachment, Terraform already destroys it first.
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

	// The route deletes with cascade: true, so this takes the volume's
	// snapshots with it. Documented on the resource page.
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
