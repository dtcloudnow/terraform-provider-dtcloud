package limit_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
)

// The response is a flat object: an id plus one number per quota. -1 means
// unlimited, and dt-go substitutes the word "Unlimited" for it before the
// provider ever sees it — this fake keeps the -1 so that conversion is actually
// exercised rather than assumed.
func fakeLimitAPI() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acctest.RequireAuth(w, r) {
			return
		}
		if r.URL.Path != "/openstack/limits/proj-1" {
			acctest.NotFound(w, "no such route: "+r.URL.Path)
			return
		}
		acctest.WriteJSON(w, http.StatusOK, map[string]any{
			"id":              "proj-1",
			"cores":           48,
			"instances":       -1,
			"ram":             98304,
			"key_pairs":       100,
			"security_groups": -1,
		})
	})
}

func TestAccDtcloudProjectLimits(t *testing.T) {
	server := httptest.NewServer(fakeLimitAPI())
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
data "dtcloud_project_limits" "mine" {
  project_id = "proj-1"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.dtcloud_project_limits.mine", "id", "proj-1"),

					// Whole numbers must not come out as "48.000000".
					resource.TestCheckResourceAttr("data.dtcloud_project_limits.mine", "quotas.cores", "48"),
					resource.TestCheckResourceAttr("data.dtcloud_project_limits.mine", "quotas.ram", "98304"),
					resource.TestCheckResourceAttr("data.dtcloud_project_limits.mine", "quotas.key_pairs", "100"),

					// -1 arrives as the word, from dt-go.
					resource.TestCheckResourceAttr("data.dtcloud_project_limits.mine", "quotas.instances", "Unlimited"),
					resource.TestCheckResourceAttr("data.dtcloud_project_limits.mine", "quotas.security_groups", "Unlimited"),

					// The id key is not a quota and must not appear among them.
					resource.TestCheckNoResourceAttr("data.dtcloud_project_limits.mine", "quotas.id"),

					resource.TestCheckResourceAttr("data.dtcloud_project_limits.mine", "names.#", "5"),
					resource.TestCheckResourceAttr("data.dtcloud_project_limits.mine", "names.0", "cores"),
				),
			},
		},
	})
}
