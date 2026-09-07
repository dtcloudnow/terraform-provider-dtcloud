package router

import (
	"context"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// DataSourceDtcloudRouter looks up one router by id.
//
// Use it to reference a router Terraform did not create — attaching a network
// to it, or reading the address its gateway holds. Declaring such a router as a
// resource instead would hand Terraform ownership of it, and a later destroy
// would take down whatever else is behind it.
//
// It reads the details endpoint, so the external network is reported by id,
// exactly as on the resource. dtcloud_routers reads a different endpoint that
// reports the same network by name.
func DataSourceDtcloudRouter() *schema.Resource {
	s := map[string]*schema.Schema{
		"id": {
			Type:         schema.TypeString,
			Required:     true,
			ValidateFunc: validation.NoZeroValues,
			Description:  "ID of the router to look up.",
		},
		"name": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "Name of the router.",
		},
		"external_network_id": {
			Type:     schema.TypeString,
			Computed: true,
			Description: "ID of the external network the router's gateway is on. Empty if the " +
				"gateway was removed outside Terraform.",
		},
		"enable_snat": {
			Type:        schema.TypeBool,
			Computed:    true,
			Description: "Whether the gateway masquerades traffic from the networks behind it.",
		},
	}
	for name, attr := range routerAttributesSchema() {
		s[name] = attr
	}

	return &schema.Resource{
		ReadContext: dataSourceDtcloudRouterRead,
		Schema:      s,
	}
}

func dataSourceDtcloudRouterRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	id := d.Get("id").(string)

	details, _, err := client.Router.GetRouterDetails(ctx, id, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			return diag.Errorf("Router %q not found", id)
		}
		return diag.Errorf("Error retrieving router %q: %s", id, err)
	}
	if details == nil || details.ID == "" {
		return diag.Errorf("Router %q not found", id)
	}

	d.SetId(details.ID)
	setRouterAttributes(d, details)

	return nil
}
