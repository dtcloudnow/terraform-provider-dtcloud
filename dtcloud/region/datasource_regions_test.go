package region_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
)

// The `metadata` block is the point of this fake. dt-go's typed response drops
// it, so the data source parses the raw body — and one region here deliberately
// has no metadata at all, because the provider has to cope with that rather
// than assume every region carries it.
func fakeRegionAPI() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acctest.RequireAuth(w, r) {
			return
		}
		if r.URL.Path != "/openstack/regions" {
			acctest.NotFound(w, "no such route")
			return
		}
		acctest.WriteJSON(w, http.StatusOK, map[string]any{
			"regions": []map[string]any{
				{
					"id": 1, "name": "Istanbul-Europe-1b (EU-SE-1b)",
					"metadata": map[string]any{
						"serviceAvailability": map[string]bool{
							"vm": true, "networks": true, "loadBalancers": true,
							"kubernetes": false,
						},
					},
				},
				{"id": 2, "name": "Ankara-1a"},
			},
		})
	})
}

func TestAccDtcloudRegions(t *testing.T) {
	server := httptest.NewServer(fakeRegionAPI())
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
data "dtcloud_regions" "all" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.dtcloud_regions.all", "regions.#", "2"),
					resource.TestCheckResourceAttr("data.dtcloud_regions.all", "regions.0.id", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_regions.all", "regions.0.name", "Istanbul-Europe-1b (EU-SE-1b)"),
					resource.TestCheckResourceAttr("data.dtcloud_regions.all", "ids.0", "1"),

					// Only the services reported as available, sorted, and
					// kubernetes:false left out.
					resource.TestCheckResourceAttr("data.dtcloud_regions.all", "regions.0.available_services.#", "3"),
					resource.TestCheckResourceAttr("data.dtcloud_regions.all", "regions.0.available_services.0", "loadBalancers"),
					resource.TestCheckResourceAttr("data.dtcloud_regions.all", "regions.0.available_services.1", "networks"),
					resource.TestCheckResourceAttr("data.dtcloud_regions.all", "regions.0.available_services.2", "vm"),

					// A region with no metadata must still appear, just without
					// a service list.
					resource.TestCheckResourceAttr("data.dtcloud_regions.all", "regions.1.name", "Ankara-1a"),
					resource.TestCheckResourceAttr("data.dtcloud_regions.all", "regions.1.available_services.#", "0"),
				),
			},
		},
	})
}
