package image

import (
	"context"
	"fmt"
	"strings"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudImage looks up one image, by id or by name.
//
// Use it to reference an image Terraform did not upload — the platform's own
// catalogue, or one somebody built by hand. Declaring such an image as a
// resource would hand Terraform ownership of it, and a later destroy would
// delete it.
//
// A lookup by name reads the list endpoint and refuses an ambiguous answer:
// image names are not unique on the platform, and quietly picking the first
// match would build machines from an image nobody chose.
func DataSourceDtcloudImage() *schema.Resource {
	s := map[string]*schema.Schema{
		"id": {
			Type:         schema.TypeString,
			Optional:     true,
			Computed:     true,
			ExactlyOneOf: []string{"id", "name"},
			Description:  "ID of the image to look up. Give this or `name`.",
		},
		"name": {
			Type:         schema.TypeString,
			Optional:     true,
			Computed:     true,
			ExactlyOneOf: []string{"id", "name"},
			Description: "Name of the image to look up. Give this or `id`. It is an error for " +
				"more than one image to carry the name.",
		},
		"os_distro": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "Distribution the image carries.",
		},
		"visibility": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "Who can see the image: `public`, `private`, `shared` or `community`.",
		},
		"uefi": {
			Type:        schema.TypeBool,
			Computed:    true,
			Description: "Whether the image boots with UEFI firmware rather than BIOS.",
		},
		"min_disk": {
			Type:     schema.TypeInt,
			Computed: true,
			Description: "Smallest volume, in GB, a machine built from this image needs. `0` when " +
				"the platform reports none.",
		},
	}
	for name, attr := range imageAttributesSchema() {
		s[name] = attr
	}

	return &schema.Resource{
		ReadContext: dataSourceDtcloudImageRead,
		Schema:      s,
	}
}

func dataSourceDtcloudImageRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	id := d.Get("id").(string)
	if id == "" {
		name := d.Get("name").(string)
		found, err := imageIDByName(ctx, client, name)
		if err != nil {
			return diag.FromErr(err)
		}
		id = found
	}

	// Both paths finish on the details endpoint, so the same fields come back
	// however the image was found. The list endpoint answers with a different
	// set under different keys, and reading it here for one of the two cases
	// would make this data source describe the same image two ways.
	details, _, err := client.Image.GetImageDetails(ctx, id, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("Image %q not found", id)
		}
		return diag.Errorf("Error retrieving image %q: %s", id, err)
	}
	if details.ID == "" {
		return diag.Errorf("Image %q not found", id)
	}

	d.SetId(details.ID)
	d.Set("name", details.Name)
	d.Set("os_distro", details.OsDistro)
	d.Set("visibility", details.Visibility)
	d.Set("uefi", details.Uefi)
	d.Set("min_disk", parseSizeGB(details.MinVolumeSize))
	setImageAttributes(d, details)

	return nil
}

// imageIDByName resolves a name against the list endpoint.
//
// Names are not unique, so anything other than exactly one match is an error:
// the alternative is a configuration that silently starts pointing at a
// different image the day somebody uploads one with the same name.
func imageIDByName(ctx context.Context, client *dtgo.Client, name string) (string, error) {
	list, _, err := client.Image.ListImages(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("error listing images to find %q: %w", name, err)
	}

	matches := []string{}
	for _, img := range list {
		if img.Name == name {
			matches = append(matches, img.ID)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no image named %q", name)
	default:
		return "", fmt.Errorf("%d images are named %q (%s); look the one you want up by id",
			len(matches), name, strings.Join(matches, ", "))
	}
}
