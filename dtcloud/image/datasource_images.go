package image

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudImages lists every image visible to the caller. Filters are
// applied here, since the API's visibility parameter only separates public from
// the rest. The list endpoint guesses `os_type` where the platform left it
// empty, so dtcloud_image can report an empty one for the same image.
func DataSourceDtcloudImages() *schema.Resource {
	return &schema.Resource{
		Description: "Lists every image visible to the caller.",

		ReadContext: dataSourceDtcloudImagesRead,
		Schema: map[string]*schema.Schema{
			"name": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return images with this exact name. Applied by the provider.",
			},
			"os_distro": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return images carrying this distribution. Applied by the provider.",
			},
			"visibility": {
				Type:     schema.TypeString,
				Optional: true,
				Description: "Only return images with this visibility. Applied by the provider, " +
					"because the endpoint's own filter only separates public images from the rest.",
			},
			"status": {
				Type:     schema.TypeString,
				Optional: true,
				Description: "Only return images in this status, e.g. `active`. Applied by the " +
					"provider.",
			},
			"images": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The images that matched, in the order the platform returned them.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":         {Type: schema.TypeString, Computed: true},
						"name":       {Type: schema.TypeString, Computed: true},
						"status":     {Type: schema.TypeString, Computed: true},
						"os_distro":  {Type: schema.TypeString, Computed: true},
						"os_type":    {Type: schema.TypeString, Computed: true, Description: "`linux` or `windows`, guessed from `os_distro` when the platform leaves it empty."},
						"visibility": {Type: schema.TypeString, Computed: true},
						"uefi":       {Type: schema.TypeBool, Computed: true},
						"type":       {Type: schema.TypeString, Computed: true, Description: "`ISO` or `Template (VM)`. A display category, not the disk format."},
						"size":       {Type: schema.TypeString, Computed: true, Description: "Formatted by the platform, e.g. `1.5 GB`."},
						"min_disk":   {Type: schema.TypeInt, Computed: true, Description: "Smallest volume in GB, parsed out of the platform's `20 GB`. `0` when it reports none."},
					},
				},
			},
		},
	}
}

func dataSourceDtcloudImagesRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	list, _, err := client.Image.ListImages(ctx, nil)
	if err != nil {
		return diag.Errorf("Error listing images: %s", err)
	}

	nameFilter := d.Get("name").(string)
	distroFilter := d.Get("os_distro").(string)
	visibilityFilter := d.Get("visibility").(string)
	statusFilter := d.Get("status").(string)

	out := make([]interface{}, 0, len(list))
	ids := make([]string, 0, len(list))
	for _, img := range list {
		if nameFilter != "" && img.Name != nameFilter {
			continue
		}
		if distroFilter != "" && !strings.EqualFold(img.OsDistro, distroFilter) {
			continue
		}
		if visibilityFilter != "" && !strings.EqualFold(img.Visibility, visibilityFilter) {
			continue
		}
		if statusFilter != "" && !strings.EqualFold(img.Status, statusFilter) {
			continue
		}
		out = append(out, map[string]interface{}{
			"id":         img.ID,
			"name":       img.Name,
			"status":     img.Status,
			"os_distro":  img.OsDistro,
			"os_type":    img.OsType,
			"visibility": img.Visibility,
			"uefi":       img.Uefi,
			"type":       img.Type,
			"size":       img.Size,
			"min_disk":   parseSizeGB(img.MinVolumeSize),
		})
		ids = append(ids, img.ID)
	}

	if err := d.Set("images", out); err != nil {
		return diag.FromErr(err)
	}

	// A stable id keeps an unchanged result from showing up as a diff.
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	d.SetId(fmt.Sprintf("%x", sum[:8]))

	return nil
}
