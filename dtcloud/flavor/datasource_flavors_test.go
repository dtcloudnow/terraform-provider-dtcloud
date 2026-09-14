package flavor_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
)

// fakeFlavorAPI stands in for the /openstack/flavors routes.
//
// The load balancer catalogue deliberately repeats a name across the two `ha`
// settings, because the real one does: that is how HA is chosen, and it is why
// a name on its own does not identify a flavor.
func fakeFlavorAPI() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acctest.RequireAuth(w, r) {
			return
		}
		switch r.URL.Path {
		case "/openstack/flavors":
			acctest.WriteJSON(w, http.StatusOK, []map[string]any{
				{"id": "flavor-small", "name": "small", "vcpus": 1, "ram": "1 GB"},
				{"id": "flavor-medium", "name": "c.medium", "vcpus": 2, "ram": "4 GB"},
			})
		default:
			acctest.NotFound(w, "no such route")
		}
	})
}

func TestAccDtcloudFlavors(t *testing.T) {
	server := httptest.NewServer(fakeFlavorAPI())
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
data "dtcloud_flavors" "all" {}

data "dtcloud_flavors" "medium" {
  name = "c.medium"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.dtcloud_flavors.all", "flavors.#", "2"),
					resource.TestCheckResourceAttr("data.dtcloud_flavors.all", "flavors.0.name", "small"),
					resource.TestCheckResourceAttr("data.dtcloud_flavors.all", "flavors.0.ram", "1 GB"),

					// Filtering happens in the provider; the endpoint takes none.
					resource.TestCheckResourceAttr("data.dtcloud_flavors.medium", "flavors.#", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_flavors.medium", "flavors.0.id", "flavor-medium"),
					resource.TestCheckResourceAttr("data.dtcloud_flavors.medium", "flavors.0.vcpus", "2"),
				),
			},
		},
	})
}
