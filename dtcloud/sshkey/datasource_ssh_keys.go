package sshkey

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceDtcloudSSHKeys lists the SSH keys on the account: name and creation
// timestamp only. Use the singular data source for fingerprints and public
// keys, which cost one call each.
func DataSourceDtcloudSSHKeys() *schema.Resource {
	return &schema.Resource{
		Description: "Lists the SSH keys on the account.",

		ReadContext: dataSourceDtcloudSSHKeysRead,
		Schema: map[string]*schema.Schema{
			"ssh_keys": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "The SSH keys visible to the caller.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:        schema.TypeString,
							Computed:    true,
							Description: "Name of the key. This is also its id — keys are addressed by name.",
						},
						"created": {
							Type:        schema.TypeString,
							Computed:    true,
							Description: "When the key was created, as reported by the API.",
						},
					},
				},
			},
			"names": {
				Type:        schema.TypeList,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "Just the names, for the common case of checking whether a key exists.",
			},
		},
	}
}

func dataSourceDtcloudSSHKeysRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	keys, _, err := client.SSHKeys.List(ctx, nil)
	if err != nil {
		return diag.Errorf("Error listing SSH keys: %s", err)
	}

	out := make([]interface{}, 0, len(keys))
	names := make([]interface{}, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]interface{}{"name": k.Name, "created": k.Created})
		names = append(names, k.Name)
	}

	if err := d.Set("ssh_keys", out); err != nil {
		return diag.FromErr(err)
	}
	if err := d.Set("names", names); err != nil {
		return diag.FromErr(err)
	}

	// A stable id keeps an unchanged list from registering as a diff.
	flat := make([]string, 0, len(names))
	for _, n := range names {
		flat = append(flat, fmt.Sprint(n))
	}
	sum := sha256.Sum256([]byte(strings.Join(flat, ",")))
	d.SetId(fmt.Sprintf("%x", sum[:8]))

	return nil
}
