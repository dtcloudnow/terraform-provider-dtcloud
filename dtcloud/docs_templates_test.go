package dtcloud

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestEveryTypeHasADocsTemplate enforces the documentation layout: every
// resource and data source keeps its page's hand-written text, subcategory
// included, in a template of its own. Without one, tfplugindocs falls back to
// the generic layout and the page lands in the "Other" group on both sites.
func TestEveryTypeHasADocsTemplate(t *testing.T) {
	p := Provider()
	kinds := map[string][]string{"resources": nil, "data-sources": nil}
	for name := range p.ResourcesMap {
		kinds["resources"] = append(kinds["resources"], name)
	}
	for name := range p.DataSourcesMap {
		kinds["data-sources"] = append(kinds["data-sources"], name)
	}

	for kind, names := range kinds {
		sort.Strings(names)
		for _, name := range names {
			path := filepath.Join("..", "templates", kind, strings.TrimPrefix(name, "dtcloud_")+".md.tmpl")
			if _, err := os.Stat(path); err != nil {
				t.Errorf("%s has no page template: add templates/%s/%s.md.tmpl",
					name, kind, strings.TrimPrefix(name, "dtcloud_"))
			}
		}
	}
}
