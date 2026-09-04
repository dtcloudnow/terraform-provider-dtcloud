// Package flavor exposes the platform's flavors — the sizing catalogue.
//
// Read-only: the API offers two GET endpoints and nothing else, so there are
// data sources here and no resources. Flavors are defined by the operator, not
// by tenants.
//
// The VM and load balancer resources already resolve a flavor name back to an
// id internally, to make import work. These data sources are the other
// direction: they let a configuration name a size instead of pasting a uuid.
package flavor

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// hashIDs gives a data source a stable id, so an unchanged result does not
// register as a diff on every plan.
func hashIDs(ids []string) string {
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	return fmt.Sprintf("%x", sum[:8])
}
