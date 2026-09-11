package volume

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudVolumes lists the volumes visible to the caller.
//
// The list endpoint reports a flatter, differently-typed shape than the details
// one: it folds the attached VM's name and status up onto the volume and sends
// the size as the string "20 GB" rather than the number 20. The size is
// normalised back to an integer here so that it means the same thing on every
// page of this provider; everything else is passed through as reported.
//
// The route accepts no query parameters, so unlike dtcloud_networks the `name`
// filter is applied by the provider after the fact. It is a convenience for the
// common "find the volume called X" case, not a smaller request.
func DataSourceDtcloudVolumes() *schema.Resource {
	return &schema.Resource{
		Description: "Lists the volumes visible to the caller.\n\n" +
			"The list endpoint reports a flatter, differently-typed shape than the details one: it folds " +
			"the attached machine's name and status up onto the volume and sends the size as the string " +
			"`\"20 GB\"` rather than the number 20. The size is normalised back to an integer here so it " +
			"means the same thing on every page of this provider; everything else is passed through as " +
			"reported.\n\n" +
			"The route accepts no query parameters, so the filters are applied by the provider after the " +
			"fact. They are a convenience, not a smaller request.",

		ReadContext: dataSourceDtcloudVolumesRead,
		Schema: map[string]*schema.Schema{
			"name": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return volumes with this exact name. Applied by the provider — the API has no filter.",
			},
			"attached_to_id": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Only return volumes attached to this VM. Applied by the provider — the API has no filter.",
			},
			"volumes": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The volumes that matched.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":                 {Type: schema.TypeString, Computed: true},
						"name":               {Type: schema.TypeString, Computed: true},
						"status":             {Type: schema.TypeString, Computed: true},
						"storage_policy":     {Type: schema.TypeString, Computed: true},
						"size":               {Type: schema.TypeInt, Computed: true, Description: "Size in GB. The API reports this as \"20 GB\"; the provider parses it."},
						"bootable":           {Type: schema.TypeBool, Computed: true},
						"volume_type":        {Type: schema.TypeString, Computed: true, Description: "HDD or CD-ROM."},
						"attached_to":        {Type: schema.TypeString, Computed: true, Description: "Name of the VM this volume is attached to."},
						"attached_to_id":     {Type: schema.TypeString, Computed: true},
						"attached_to_status": {Type: schema.TypeString, Computed: true},
						"is_detachable":      {Type: schema.TypeBool, Computed: true},
					},
				},
			},
		},
	}
}

func dataSourceDtcloudVolumesRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	volumes, _, err := client.Volume.ListVolumes(ctx, nil)
	if err != nil {
		return diag.Errorf("Error listing volumes: %s", err)
	}

	nameFilter := d.Get("name").(string)
	attachedFilter := d.Get("attached_to_id").(string)

	out := make([]interface{}, 0, len(volumes))
	ids := make([]string, 0, len(volumes))
	for _, v := range volumes {
		if nameFilter != "" && v.Name != nameFilter {
			continue
		}
		if attachedFilter != "" && v.AttachedToID != attachedFilter {
			continue
		}
		out = append(out, map[string]interface{}{
			"id":             v.ID,
			"name":           v.Name,
			"status":         v.Status,
			"storage_policy": v.Policy,
			// "20 GB" -> 20. The details endpoint sends this as a number, and a
			// data source that disagreed with the resource about the type of
			// `size` would be a trap.
			"size":               parseSizeGB(v.Size),
			"bootable":           parseBootable(v.Bootable),
			"volume_type":        v.Type,
			"attached_to":        v.AttachedTo,
			"attached_to_id":     v.AttachedToID,
			"attached_to_status": v.VMStatus,
			"is_detachable":      v.IsDetachable,
		})
		ids = append(ids, v.ID)
	}

	if err := d.Set("volumes", out); err != nil {
		return diag.FromErr(err)
	}

	// A stable id keeps an unchanged result from showing up as a diff.
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	d.SetId(fmt.Sprintf("%x", sum[:8]))

	return nil
}
