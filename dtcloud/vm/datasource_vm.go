package vm

import (
	"context"
	"fmt"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// DataSourceDtcloudVM looks up a single VM by id. Names are not unique — the
// multi-create path generates `name-1`, `name-2`, … — so a name cannot resolve
// to one instance.
func DataSourceDtcloudVM() *schema.Resource {
	dsSchema := map[string]*schema.Schema{
		"id": {
			Type:         schema.TypeString,
			Required:     true,
			ValidateFunc: validation.NoZeroValues,
			Description:  "ID of the virtual machine to look up.",
		},
		"name": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "Name of the virtual machine.",
		},
	}
	for name, s := range vmComputedSchema() {
		if _, exists := dsSchema[name]; exists {
			continue
		}
		dsSchema[name] = s
	}

	return &schema.Resource{
		ReadContext: dataSourceDtcloudVMRead,
		Schema:      dsSchema,
	}
}

func dataSourceDtcloudVMRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	id := d.Get("id").(string)

	details, _, err := client.VirtualMachine.GetVirtualMachineDetails(ctx, id, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("VM %q not found", id)
		}
		return diag.Errorf("Error retrieving VM %q: %s", id, err)
	}
	if details.ID == "" {
		return diag.Errorf("VM %q not found", id)
	}

	d.SetId(details.ID)
	setVMAttributes(d, details)

	// The interfaces and volumes live behind their own endpoints, same as in the
	// resource — without this the data source would advertise the attributes but
	// never fill them in.
	var diags diag.Diagnostics
	for _, err := range readVMAttachments(ctx, client, d, details.ID, details.ImageID) {
		diags = append(diags, diag.Diagnostic{
			Severity: diag.Warning,
			Summary:  fmt.Sprintf("Could not read part of VM %q", id),
			Detail:   err.Error(),
		})
	}

	return diags
}
