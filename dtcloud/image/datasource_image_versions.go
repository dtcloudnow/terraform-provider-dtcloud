package image

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudImageVersions reads the platform's own image catalogue.
//
// This is not the same list as dtcloud_images. That one is what the image
// service holds; this one is the curated set the platform offers, and it is
// the only place that says which flavors an image may be built on.
//
// The endpoint answers with a map keyed by family, each holding one entry per
// version. It is flattened into a single list here, sorted by family and then
// by version, because a map keyed by a value from the platform would reshuffle
// a plan every time the catalogue gained an entry.
func DataSourceDtcloudImageVersions() *schema.Resource {
	return &schema.Resource{
		Description: "Reads the platform's own image catalogue.\n\n" +
			"This is not the same list as `dtcloud_images`. That one is what the image service holds; " +
			"this one is the curated set the platform offers, and it is the only place that says which " +
			"flavors an image may be built on.\n\n" +
			"The endpoint answers with a map keyed by family, each holding one entry per version. It is " +
			"flattened into a single list here, sorted by family and then by version, because a map keyed " +
			"by a value from the platform would reshuffle a plan every time the catalogue gained an " +
			"entry.",

		ReadContext: dataSourceDtcloudImageVersionsRead,
		Schema: map[string]*schema.Schema{
			"type": {
				Type:     schema.TypeString,
				Optional: true,
				Description: "Only return entries in this family, e.g. `ubuntu`. Applied by the " +
					"provider; the endpoint takes no parameters.",
			},
			"versions": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "Catalogue entries, sorted by family and then by version.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"type":            {Type: schema.TypeString, Computed: true, Description: "Family the entry belongs to, e.g. `ubuntu`."},
						"id":              {Type: schema.TypeString, Computed: true, Description: "ID of the image, for `block_device` on `dtcloud_vm`."},
						"version":         {Type: schema.TypeString, Computed: true},
						"image_type":      {Type: schema.TypeString, Computed: true, Description: "`ISO` or `Template (VM)` — the same categories as `type` on `dtcloud_image`, not an operating system."},
						"min_volume_size": {Type: schema.TypeInt, Computed: true, Description: "Smallest volume in GB. A number here, unlike everywhere else in the image endpoints."},
						"valid_flavor_ids": {
							Type:     schema.TypeList,
							Computed: true,
							Elem:     &schema.Schema{Type: schema.TypeString},
							Description: "Flavors the entry may be built on. Empty when the " +
								"catalogue places no restriction on it.",
						},
					},
				},
			},
		},
	}
}

func dataSourceDtcloudImageVersionsRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	versions, _, err := client.Image.GetImageVersions(ctx, nil)
	if err != nil {
		return diag.Errorf("Error listing image versions: %s", err)
	}

	typeFilter := d.Get("type").(string)

	families := make([]string, 0, len(versions))
	for family := range versions {
		families = append(families, family)
	}
	sort.Strings(families)

	out := []interface{}{}
	ids := []string{}
	for _, family := range families {
		if typeFilter != "" && !strings.EqualFold(family, typeFilter) {
			continue
		}
		entries := versions[family]
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Version < entries[j].Version })
		for _, e := range entries {
			out = append(out, map[string]interface{}{
				// Two different things are called `type` by the endpoint: the
				// map key is the family, and the entry's own field is the same
				// display category the image endpoints report. Only the first
				// keeps the name.
				"type":             family,
				"id":               e.ID,
				"version":          e.Version,
				"image_type":       e.Type,
				"min_volume_size":  e.MinVolumeSize,
				"valid_flavor_ids": flattenFlavorIDs(e.ValidFlavorIds),
			})
			ids = append(ids, e.ID)
		}
	}

	if err := d.Set("versions", out); err != nil {
		return diag.FromErr(err)
	}

	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	d.SetId(fmt.Sprintf("%x", sum[:8]))

	return nil
}

// flattenFlavorIDs renders the catalogue's flavor list as strings.
//
// The field is stored by the platform rather than produced by OpenStack, and
// the SDK models it as an untyped value. Every entry seen so far holds null —
// the list, the comma-separated string and the single value are handled because
// the column can carry them, not because one has been observed. A data source
// that assumed the shape would fail a read for an entry somebody else wrote.
func flattenFlavorIDs(raw interface{}) []string {
	out := []string{}
	switch v := raw.(type) {
	case nil:
		return out
	case []interface{}:
		for _, item := range v {
			if s := flavorIDString(item); s != "" {
				out = append(out, s)
			}
		}
	case string:
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	default:
		if s := flavorIDString(v); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// flavorIDString renders one flavor id. Numbers come back through JSON as
// floats, and rendering those with %v would turn 42 into 42 but 1e+06 into
// something no flavor is called.
func flavorIDString(raw interface{}) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return ""
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}
