package image

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

// ResourceDtcloudImage manages a disk image captured from a volume.
//
// An image is created by asking a volume to become one, not by uploading a local
// file: the upload endpoint answers 403 for customer accounts. Build the
// contents by making a volume, then capture it.
//
// In place: name, os_distro, min_disk and visibility. Everything else is
// ForceNew — nothing replaces the data of an existing image.
//
// `visibility = "public"` is refused, and `protected` is not exposed: it is
// accepted at create time but cannot be cleared, which would leave the resource
// impossible to destroy.
func ResourceDtcloudImage() *schema.Resource {
	s := map[string]*schema.Schema{
		"name": {
			Type:     schema.TypeString,
			Required: true,
			ValidateFunc: validation.All(
				validation.NoZeroValues,
				validation.StringMatch(regexp.MustCompile(noHTMLPattern),
					`must not contain any of < > & " '`),
			),
			Description: "Name of the image. Can be changed in place. Names are not unique on " +
				"the platform, which is why `dtcloud_image` prefers a lookup by id.",
		},
		"source_volume_id": {
			Type:         schema.TypeString,
			Required:     true,
			ForceNew:     true,
			ValidateFunc: validation.IsUUID,
			Description: "Volume to capture. Its contents at the moment of the call become the " +
				"image; later writes to the volume do not reach it. The volume survives, and " +
				"the image outlives it. There is no endpoint that replaces the data of an " +
				"existing image, so pointing this at another volume builds a new one.",
		},
		"container_format": {
			Type:         schema.TypeString,
			Optional:     true,
			Default:      "bare",
			ForceNew:     true,
			ValidateFunc: validation.NoZeroValues,
			Description: "Container wrapped around the disk data, `bare` unless you have a " +
				"reason. Write-only: no endpoint reports it back.",
		},
		"disk_format": {
			Type:         schema.TypeString,
			Required:     true,
			ForceNew:     true,
			ValidateFunc: validation.NoZeroValues,
			Description: "Format of the file, such as `qcow2` or `iso`. The accepted values come " +
				"from the platform's own configuration rather than from a list in the provider, " +
				"so an unsupported one is refused by the API during apply. Not the same thing " +
				"as the `type` attribute, which is a display category derived from it.",
		},
		"os_distro": {
			Type:         schema.TypeString,
			Optional:     true,
			Computed:     true,
			ValidateFunc: validation.NoZeroValues,
			Description: "Distribution the image carries, such as `debian12`. The capture takes " +
				"this from the source volume, so leaving it out inherits whatever the volume " +
				"was built from. Set it to override, on create or later; the accepted values " +
				"are the platform's and are checked by the API.",
		},
		"min_disk": {
			Type:         schema.TypeInt,
			Optional:     true,
			Computed:     true,
			ValidateFunc: validation.IntBetween(1, 512),
			Description: "Smallest volume, in GB, a machine built from this image needs. The " +
				"capture derives it from the size of the source volume; set it to override, on " +
				"create or later. The API reports it as the text `20 GB`, which the provider " +
				"parses back into a number.",
		},
		"visibility": {
			Type:     schema.TypeString,
			Optional: true,
			Default:  "shared",
			ValidateFunc: validation.StringInSlice(
				[]string{"private", "shared", "community"}, false),
			Description: "Who can see the image: `private`, `shared` or `community`. Defaults " +
				"to `shared`. Can be changed in place. `public` is not accepted: publishing " +
				"needs the `publicize_image` permission, which customer accounts are not " +
				"granted, and the platform answers 403 on both the capture and the update.",
		},
		// uefi is reported back but nothing accepts it, on either endpoint.
		"uefi": {
			Type:     schema.TypeBool,
			Computed: true,
			Description: "Whether the image boots with UEFI firmware. Inherited from the source " +
				"volume; there is no endpoint that sets it.",
		},
	}
	for name, attr := range imageAttributesSchema() {
		s[name] = attr
	}

	return &schema.Resource{
		Description: "Manages a disk image and the file behind it.",

		CreateContext: resourceDtcloudImageCreate,
		ReadContext:   resourceDtcloudImageRead,
		UpdateContext: resourceDtcloudImageUpdate,
		DeleteContext: resourceDtcloudImageDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		Schema: s,

		Timeouts: &schema.ResourceTimeout{
			// Create covers waiting for the volume, the capture and the copy that
			// follows it, which scales with the size of the volume.
			Create: schema.DefaultTimeout(2 * time.Hour),
			Update: schema.DefaultTimeout(15 * time.Minute),
			Delete: schema.DefaultTimeout(30 * time.Minute),
		},
	}
}

func resourceDtcloudImageCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	name := d.Get("name").(string)
	volumeID := d.Get("source_volume_id").(string)

	// The capture is refused unless the volume is `available`, and one already
	// running holds it, so a second image off the same volume fails rather than
	// queueing.
	if err := waitForVolumeAvailable(ctx, client, volumeID, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.Errorf("Error waiting for volume %q to be available for capture: %s", volumeID, err)
	}

	// The action reports nothing about what it made: 200, an empty body and no
	// Location header. Names are not unique, so the ids that already exist are
	// recorded first.
	before, err := imageIDs(ctx, client)
	if err != nil {
		return diag.Errorf("Error listing images before capturing volume %q: %s", volumeID, err)
	}

	action := dtgo.PerformActionParams{
		OsUploadImage: &dtgo.OsUploadImage{
			ImageName:       name,
			DiskFormat:      d.Get("disk_format").(string),
			ContainerFormat: d.Get("container_format").(string),
			Visibility:      d.Get("visibility").(string),
		},
	}
	if _, err := client.Volume.PerformActionOnVolume(ctx, volumeID, action, nil); err != nil {
		return diag.Errorf("Error capturing volume %q as image %q: %s", volumeID, name, err)
	}

	id, err := findCapturedImage(ctx, client, name, before, d.Timeout(schema.TimeoutCreate))
	if err != nil {
		return diag.Errorf("Error finding the image captured from volume %q: %s\n\n"+
			"The capture itself was accepted, so an image named %q may well exist; it is not "+
			"in Terraform's state and has to be imported or removed by hand.", volumeID, err, name)
	}
	d.SetId(id)

	if err := waitForImage(ctx, client, id, d.Timeout(schema.TimeoutCreate), nil); err != nil {
		return diag.Errorf("Error waiting for image %q to become active: %s", id, err)
	}

	// The capture inherits the distribution and min_disk from the volume, so
	// anything the configuration asked for is applied afterwards.
	if diags := applyImageOverrides(ctx, d, client); diags != nil {
		return diags
	}

	return resourceDtcloudImageRead(ctx, d, meta)
}

// applyImageOverrides patches the fields the capture cannot take, but only when
// the configuration actually set them - left out, they keep what was inherited.
func applyImageOverrides(ctx context.Context, d *schema.ResourceData, client *dtgo.Client) diag.Diagnostics {
	overrides := []struct {
		key   string
		path  string
		value interface{}
	}{
		{"os_distro", "/os_distro", d.Get("os_distro")},
		{"min_disk", "/min_disk", d.Get("min_disk")},
	}

	for _, o := range overrides {
		if _, set := d.GetOkExists(o.key); !set {
			continue
		}
		params := dtgo.PerformImageActionParams{Op: "replace", Path: o.path, Value: o.value}
		if _, err := client.Image.PerformImageAction(ctx, d.Id(), params, nil); err != nil {
			return diag.Errorf("Error setting %s on the captured image %q: %s", o.key, d.Id(), err)
		}
	}
	return nil
}

func resourceDtcloudImageRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	details, _, err := client.Image.GetImageDetails(ctx, d.Id(), nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error retrieving image %q: %s", d.Id(), err)
	}
	if details.ID == "" {
		d.SetId("")
		return nil
	}

	d.Set("name", details.Name)
	d.Set("os_distro", details.OsDistro)
	d.Set("visibility", details.Visibility)
	d.Set("uefi", details.Uefi)

	// min_disk arrives as a formatted string, or "-" when there is none. The
	// configured value is kept in that case rather than reading back as 0.
	if minDisk := parseSizeGB(details.MinVolumeSize); minDisk > 0 {
		d.Set("min_disk", minDisk)
	}

	// disk_format and container_format are absent from every read endpoint, so
	// they keep whatever the configuration last set. After an import they are
	// empty and the first plan wants to replace the image to "fix" that, which
	// is why the lifecycle test ignores them on import.

	setImageAttributes(d, details)

	return nil
}

func resourceDtcloudImageUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	// One field per request, only these four, in a fixed order so a partial
	// failure is repeatable.
	updates := []struct {
		key   string
		path  string
		value interface{}
	}{
		{"name", "/name", d.Get("name").(string)},
		{"os_distro", "/os_distro", d.Get("os_distro").(string)},
		{"min_disk", "/min_disk", d.Get("min_disk").(int)},
		{"visibility", "/visibility", d.Get("visibility").(string)},
	}

	for _, u := range updates {
		if !d.HasChange(u.key) {
			continue
		}
		params := dtgo.PerformImageActionParams{Op: "replace", Path: u.path, Value: u.value}
		if _, err := client.Image.PerformImageAction(ctx, d.Id(), params, nil); err != nil {
			return diag.Errorf("Error updating %s on image %q: %s", u.key, d.Id(), err)
		}
	}

	// Wait on the values that were asked for rather than on the status: an image
	// stays `active` throughout an update.
	name := d.Get("name").(string)
	osDistro := d.Get("os_distro").(string)
	minDisk := d.Get("min_disk").(int)
	visibility := d.Get("visibility").(string)
	settled := func(img dtgo.GetImageDetails) bool {
		return img.Name == name &&
			img.OsDistro == osDistro &&
			parseSizeGB(img.MinVolumeSize) == minDisk &&
			img.Visibility == visibility
	}
	if err := waitForImage(ctx, client, d.Id(), d.Timeout(schema.TimeoutUpdate), settled); err != nil {
		return diag.Errorf("Error waiting for image %q to be updated: %s", d.Id(), err)
	}

	return resourceDtcloudImageRead(ctx, d, meta)
}

func resourceDtcloudImageDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	if _, err := client.Image.DeleteImage(ctx, d.Id(), nil); err != nil {
		// Already gone is a successful destroy, not a failure.
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		// The platform refuses to delete a protected image and cannot unmark one,
		// so its own message is passed through with the reason it does not name.
		return diag.Errorf("Error deleting image %q: %s\n\n"+
			"An image marked protected cannot be deleted through this API, and this provider "+
			"never marks one; if that is the reason, it has to be cleared elsewhere.", d.Id(), err)
	}

	if err := waitForImageGone(ctx, client, d.Id(), d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.Errorf("Error waiting for image %q to be deleted: %s", d.Id(), err)
	}

	d.SetId("")
	return nil
}
