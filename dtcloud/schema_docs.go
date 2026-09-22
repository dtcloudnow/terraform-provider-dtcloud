package dtcloud

import (
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// forceNewNote is appended to the description of every argument that forces
// replacement. The schema Terraform exports -- which is what the documentation
// is generated from -- does not say which arguments are ForceNew, so without
// this the docs could only mention it where someone remembered to write it.
const forceNewNote = "Changing this forces a new resource to be created."

// annotateForceNew walks every resource schema, nested blocks included, and
// appends forceNewNote to each ForceNew argument that does not already say so.
func annotateForceNew(p *schema.Provider) {
	for _, r := range p.ResourcesMap {
		annotateForceNewIn(r.Schema)
	}
}

func annotateForceNewIn(m map[string]*schema.Schema) {
	for _, s := range m {
		if s.ForceNew && (s.Required || s.Optional) && !mentionsReplacement(s.Description) {
			desc := strings.TrimSpace(s.Description)
			if desc != "" && !strings.HasSuffix(desc, ".") {
				desc += "."
			}
			s.Description = strings.TrimSpace(desc + " " + forceNewNote)
		}
		if r, ok := s.Elem.(*schema.Resource); ok {
			annotateForceNewIn(r.Schema)
		}
	}
}

// mentionsReplacement reports whether a description already says that
// changing the argument replaces the resource, so the note is not doubled --
// including when two resources share the same schema.
func mentionsReplacement(desc string) bool {
	d := strings.ToLower(desc)
	return strings.Contains(d, "recreat") ||
		strings.Contains(d, "forces a new") ||
		strings.Contains(d, "forces replacement")
}
