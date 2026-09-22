package dtcloud

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestIndexListsEveryType keeps the hand-written tables of resources and data
// sources in step with the provider. Neither table can be generated -- a page
// template only sees its own type -- so both are written out by hand, and this
// is what stops them going stale when a service is added.
func TestIndexListsEveryType(t *testing.T) {
	for _, path := range [][]string{
		{"..", "templates", "index.md.tmpl"},
		{"..", "README.md"},
	} {
		name := filepath.Join(path...)
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(name)
			if err != nil {
				t.Fatalf("reading %s: %s", name, err)
			}
			table := string(raw)

			p := Provider()
			var missing []string
			for typeName := range p.ResourcesMap {
				if !strings.Contains(table, fmt.Sprintf("`%s`", typeName)) {
					missing = append(missing, typeName)
				}
			}
			for typeName := range p.DataSourcesMap {
				if !strings.Contains(table, fmt.Sprintf("`%s`", typeName)) {
					missing = append(missing, typeName+" (data source)")
				}
			}

			sort.Strings(missing)
			for _, typeName := range missing {
				t.Errorf("%s is missing from the table in %s", typeName, name)
			}
		})
	}
}
