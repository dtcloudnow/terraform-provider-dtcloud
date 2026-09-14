package sshkey

import (
	"context"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudSSHKey manages an SSH key. There is no update, and keys are
// addressed by name, so the Terraform ID is the key name and both `name` and
// `public_key` are ForceNew.
func ResourceDtcloudSSHKey() *schema.Resource {
	return &schema.Resource{
		Description: "Manages an SSH key that can be injected into a virtual machine at boot.",

		CreateContext: resourceDtcloudSSHKeyCreate,
		ReadContext:   resourceDtcloudSSHKeyRead,
		DeleteContext: resourceDtcloudSSHKeyDelete,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "The name of the SSH key. Used as the resource identifier.",
			},
			"public_key": {
				Type:             schema.TypeString,
				Required:         true,
				ForceNew:         true,
				DiffSuppressFunc: sshKeyPublicKeyDiffSuppress,
				ValidateFunc:     validation.NoZeroValues,
				Description:      "The public key material.",
			},
			"fingerprint": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The fingerprint of the SSH key.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "When the SSH key was created, in RFC 3339 format.",
			},
			"user_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The ID of the user that owns the SSH key.",
			},
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(2 * time.Minute),
		},
	}
}

// sshKeyPublicKeyDiffSuppress ignores trailing-whitespace differences so a
// trailing newline from file("...") does not cause a perpetual diff.
func sshKeyPublicKeyDiffSuppress(k, old, new string, d *schema.ResourceData) bool {
	return strings.TrimSpace(old) == strings.TrimSpace(new)
}

func resourceDtcloudSSHKeyCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	opts := dtgo.CreateSshKeyParams{
		Name:      d.Get("name").(string),
		PublicKey: d.Get("public_key").(string),
	}

	_, _, err := client.SSHKeys.Create(ctx, opts, nil)
	if err != nil {
		return diag.Errorf("Error creating SSH key: %s", err)
	}

	d.SetId(opts.Name)

	return resourceDtcloudSSHKeyRead(ctx, d, meta)
}

func resourceDtcloudSSHKeyRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	details, _, err := client.SSHKeys.GetDetails(ctx, d.Id(), nil)
	if err != nil {
		// If the key is gone, drop it from state rather than erroring.
		if isNotFoundErr(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error retrieving SSH key %q: %s", d.Id(), err)
	}

	// A soft-deleted key should also be treated as gone.
	if details == nil || details.Keypair.Deleted {
		d.SetId("")
		return nil
	}

	kp := details.Keypair
	d.Set("name", kp.Name)
	d.Set("public_key", kp.PublicKey)
	d.Set("fingerprint", kp.Fingerprint)
	d.Set("created_at", formatTime(kp.CreatedAt))
	d.Set("user_id", kp.UserID)

	return nil
}

func resourceDtcloudSSHKeyDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	_, err := client.SSHKeys.Delete(ctx, d.Id(), nil)
	if err != nil && !isNotFoundErr(err) {
		return diag.Errorf("Error deleting SSH key %q: %s", d.Id(), err)
	}

	d.SetId("")
	return nil
}
