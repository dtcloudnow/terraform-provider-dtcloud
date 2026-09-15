package sshkey

import (
	"context"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudSSHKey looks up a single SSH key by name.
func DataSourceDtcloudSSHKey() *schema.Resource {
	return &schema.Resource{
		Description: "Looks up a single SSH key by name.",

		ReadContext: dataSourceDtcloudSSHKeyRead,
		Schema:      sshKeySchema(),
	}
}

func dataSourceDtcloudSSHKeyRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	name := d.Get("name").(string)

	details, _, err := client.SSHKeys.GetDetails(ctx, name, nil)
	if err != nil {
		if isNotFoundErr(err) {
			return diag.Errorf("SSH key %q not found", name)
		}
		return diag.Errorf("Error retrieving SSH key %q: %s", name, err)
	}
	if details == nil || details.Keypair.Deleted {
		return diag.Errorf("SSH key %q not found", name)
	}

	kp := details.Keypair
	d.SetId(kp.Name)
	d.Set("name", kp.Name)
	d.Set("public_key", kp.PublicKey)
	d.Set("fingerprint", kp.Fingerprint)
	d.Set("created_at", formatTime(kp.CreatedAt))
	d.Set("user_id", kp.UserID)

	return nil
}
