package sshkey

import (
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// sshKeySchema is the shared attribute set for the ssh key data source.
func sshKeySchema() map[string]*schema.Schema {
	return map[string]*schema.Schema{
		"name": {
			Type:        schema.TypeString,
			Required:    true,
			Description: "The name of the SSH key.",
		},
		"public_key": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "The public key material.",
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
	}
}

// formatTime renders an API timestamp for Terraform state. A timestamp that was
// absent, null or in a format dtgo could not parse reads as the empty string
// rather than a misleading zero date.
func formatTime(t dtgo.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// isNotFoundErr delegates to the shared helper so the resource, the data source
// and the vm package all agree on what "gone" looks like.
func isNotFoundErr(err error) bool {
	return dterr.IsNotFound(err)
}
