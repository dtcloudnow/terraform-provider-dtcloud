package dtcloud

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestIndexListsEveryType keeps the landing page's table of resources and data
// sources in step with the provider. The table cannot be generated -- a page
// template only sees its own type -- so it is written out in
// templates/index.md.tmpl, and this is what stops it going stale.
func TestIndexListsEveryType(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "templates", "index.md.tmpl"))
	if err != nil {
		t.Fatalf("reading the index template: %s", err)
	}
	tmpl := string(raw)

	p := Provider()
	var missing []string
	for name := range p.ResourcesMap {
		if !strings.Contains(tmpl, fmt.Sprintf("`%s`", name)) {
			missing = append(missing, name)
		}
	}
	for name := range p.DataSourcesMap {
		if !strings.Contains(tmpl, fmt.Sprintf("`%s`", name)) {
			missing = append(missing, name+" (data source)")
		}
	}

	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("%s is missing from the table in templates/index.md.tmpl", name)
	}
}
