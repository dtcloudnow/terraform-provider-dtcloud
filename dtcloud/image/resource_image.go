package image

import (
	"context"
	"os"
	"regexp"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudImage manages a disk image and the file behind it.
//
// Creating one is two requests and a wait: the first opens an empty record,
// the second uploads the file, and the image is only usable once it reaches
// `active`. Both happen inside Create, because an image with no data is a
// record no machine can boot from.
//
// In place: name, os_distro, min_disk and visibility — the four fields the
// update endpoint accepts, one request each. Everything else is ForceNew,
// including the file: there is no endpoint that replaces the data of an
// existing image.
//
// `protected` is deliberately not exposed. The API accepts it at create time
// but its update endpoint cannot clear it, so a protected image cannot be
// deleted through this API at all — an argument that quietly made a resource
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
		"source_file": {
			Type:         schema.TypeString,
			Required:     true,
			ForceNew:     true,
			ValidateFunc: validation.NoZeroValues,
			Description: "Path to the local disk file to upload. The file is read during apply " +
				"and must still be there. There is no endpoint that replaces the data of an " +
				"existing image, so changing this builds a new one.",
		},
		"source_file_hash": {
			Type:     schema.TypeString,
			Optional: true,
			ForceNew: true,
			Description: "Hash of the file, used to notice that its contents changed while its " +
				"path did not — set it to `filesha256(var.path)` or similar. Nothing reads the " +
				"file during plan, so without this a modified file goes unnoticed.",
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
			Required:     true,
			ValidateFunc: validation.NoZeroValues,
			Description: "Distribution the image carries, such as `ubuntu`. Like `disk_format`, " +
				"the accepted values are the platform's and are checked by the API. Can be " +
				"changed in place.",
		},
		"min_disk": {
			Type:         schema.TypeInt,
			Required:     true,
			ValidateFunc: validation.IntBetween(1, 512),
			Description: "Smallest volume, in GB, a machine built from this image needs. Can be " +
				"changed in place. The API reports it as the text `20 GB`, which the provider " +
				"parses back into a number.",
		},
		"visibility": {
			Type:         schema.TypeString,
			Optional:     true,
			Default:      "shared",
			ValidateFunc: validation.StringInSlice(visibilities, false),
			Description: "Who can see the image: `public`, `private`, `shared` or `community`. " +
				"Defaults to `shared`, which is what the API applies when the field is omitted. " +
				"Can be changed in place.",
		},
		"min_ram": {
			Type:         schema.TypeInt,
			Optional:     true,
			ForceNew:     true,
			ValidateFunc: validation.IntAtLeast(0),
			Description: "Smallest amount of RAM, in MB, a machine built from this image needs. " +
				"Write-only: no endpoint reports it back, so it cannot drift and does not " +
				"survive an import.",
		},
		"tags": {
			Type:     schema.TypeSet,
			Optional: true,
			ForceNew: true,
			Elem:     &schema.Schema{Type: schema.TypeString},
			Description: "Tags to attach to the image. Write-only, like `min_ram`: accepted on " +
				"create and reported by nothing.",
		},
		"uefi": {
			Type:     schema.TypeBool,
			Optional: true,
			Default:  false,
			ForceNew: true,
			Description: "Boot the image with UEFI firmware instead of BIOS. Unlike the other " +
				"create-only arguments this one is reported back, so a change made elsewhere " +
				"shows up as drift.",
		},
	}
	for name, attr := range imageAttributesSchema() {
		s[name] = attr
	}

	return &schema.Resource{
		Description: "Manages a disk image and the file behind it.\n\n" +
			"Creating one is two requests and a wait: the first opens an empty record, the second uploads " +
			"the file, and the image is usable only once it reaches `active`. Both happen during apply, " +
			"because an image with no data is a record no machine can boot from.\n\n" +
			"`name`, `os_distro`, `min_disk` and `visibility` change in place. Everything else forces " +
			"replacement, including the file: no endpoint replaces the data of an existing image.\n\n" +
			"`protected` is deliberately not exposed. The API accepts it at create time but cannot clear " +
			"it afterwards, which would make the image impossible to destroy through this provider.",

		CreateContext: resourceDtcloudImageCreate,
		ReadContext:   resourceDtcloudImageRead,
		UpdateContext: resourceDtcloudImageUpdate,
		DeleteContext: resourceDtcloudImageDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		Schema: s,

		Timeouts: &schema.ResourceTimeout{
			// Create covers the upload itself, which is as long as it takes to
			// send the file, so the default is generous.
			Create: schema.DefaultTimeout(2 * time.Hour),
			Update: schema.DefaultTimeout(15 * time.Minute),
			Delete: schema.DefaultTimeout(30 * time.Minute),
		},
	}
}

func resourceDtcloudImageCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	sourceFile := d.Get("source_file").(string)
	info, err := os.Stat(sourceFile)
	if err != nil {
		return diag.Errorf("Error reading the image file %q: %s", sourceFile, err)
	}
	if info.IsDir() {
		return diag.Errorf("Error reading the image file %q: it is a directory", sourceFile)
	}

	tags := []string{}
	for _, t := range d.Get("tags").(*schema.Set).List() {
		tags = append(tags, t.(string))
	}

	params := dtgo.CreateImageParams{
		Name:       d.Get("name").(string),
		Type:       d.Get("disk_format").(string),
		OsDistro:   d.Get("os_distro").(string),
		MinDisk:    d.Get("min_disk").(int),
		MinRAM:     d.Get("min_ram").(int),
		Visibility: d.Get("visibility").(string),
		Uefi:       dtgo.PtrTo(d.Get("uefi").(bool)),
		// Declared up front so an image that is too large for the platform is
		// refused now rather than after the whole file has been sent.
		FileSize: info.Size(),
	}
	if len(tags) > 0 {
		params.Tags = tags
	}

	body, err := client.Image.CreateImage(ctx, params, nil)
	if err != nil {
		return diag.Errorf("Error creating image %q: %s", params.Name, err)
	}

	id, err := createdImageID(body)
	if err != nil {
		return diag.Errorf("Error reading the created image's id: %s", err)
	}
	d.SetId(id)

	// The record exists but holds no data: it is `queued` until the file has
	// been uploaded, and nothing can be built from it in the meantime.
	//
	// If this fails the platform removes the image it just created, so the id
	// in state points at nothing. That is deliberate rather than repaired here:
	// Terraform marks the resource tainted, and the destroy it runs before
	// rebuilding is answered with a 404, which Delete already treats as done.
	if _, err := client.Image.UploadImage(ctx, id, sourceFile, nil); err != nil {
		return diag.Errorf("Error uploading %q to image %q: %s\n\n"+
			"The platform deletes an image whose upload fails, so this one no longer exists; "+
			"the next apply will create it again.", sourceFile, id, err)
	}

	if err := waitForImage(ctx, client, id, d.Timeout(schema.TimeoutCreate), nil); err != nil {
		return diag.Errorf("Error waiting for image %q to become active: %s", id, err)
	}

	return resourceDtcloudImageRead(ctx, d, meta)
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

	// min_disk comes back inside a formatted string, and as "-" when the
	// platform has none to report. Leaving the configured value alone in that
	// case keeps a plan from proposing a change to 0, which the API would
	// reject anyway.
	if minDisk := parseSizeGB(details.MinVolumeSize); minDisk > 0 {
		d.Set("min_disk", minDisk)
	}

	// disk_format, min_ram and tags are absent from every read endpoint, so
	// there is nothing to compare them against; they keep whatever the
	// configuration last set. After an import they are empty.

	setImageAttributes(d, details)

	return nil
}

func resourceDtcloudImageUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	// The update endpoint takes one field per request, as a path/value pair,
	// and accepts only these four. They go out in a fixed order so a failure
	// halfway through leaves the same partial state every time; the read at the
	// end reports whatever actually landed.
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

	// Wait on the values that were asked for rather than on the status: an
	// image stays `active` throughout an update, so a status-only wait would be
	// satisfied by one that still carried every old value.
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
		// The platform refuses to delete an image that is marked protected, and
		// its update endpoint cannot unmark one. The API's own message is
		// passed through, with the reason it does not name.
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
