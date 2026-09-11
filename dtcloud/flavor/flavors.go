// Package flavor exposes the sizing catalogue. Read-only, since flavors are
// defined by the operator. The data sources resolve a name to an id so a
// configuration can name a size instead of carrying a uuid.
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
