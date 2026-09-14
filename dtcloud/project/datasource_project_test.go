package project_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
)

// fakeProjectAPI stands in for the /openstack/projects routes.
//
// The quota endpoint carries the region in its *path*, unlike every other call
// in this provider, which takes it as the serverId query parameter. The fake
// checks the path segment so a provider that forgets to pass it fails here.
func fakeProjectAPI(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acctest.RequireAuth(w, r) {
			return
		}
		switch r.URL.Path {
		case "/openstack/projects":
			acctest.WriteJSON(w, http.StatusOK, map[string]any{
				"lastServerId":  1,
				"lastProjectId": "proj-2",
				"lastProject":   "openstack-stg",
				"projects": []map[string]any{
					{"name": "openstack-test-1", "id": "proj-1", "domainId": "dom-1"},
					{"name": "openstack-stg", "id": "proj-2", "domainId": "dom-1"},
				},
			})
		case "/openstack/projects/quota/1/proj-2":
			acctest.WriteJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{
					// -1 is the platform's "unlimited". It is passed through as
					// -1 rather than translated, so arithmetic stays honest.
					"cpu":         map[string]any{"usage": 24, "quota": -1},
					"ram":         map[string]any{"usage": 65, "quota": 128},
					"floatingIPs": map[string]any{"usage": 0, "quota": -1},
					"storageSpace": []map[string]any{
						{"name": "HI-IOPS", "usage": 2048845, "quota": -1},
						{"name": "default", "usage": 1012, "quota": 5000},
					},
					"vmStatus": map[string]any{
						"count": 11, "error": 0, "inProgress": 0, "running": 10, "stopped": 1,
					},
					"topVms": map[string]any{"vcpu": []any{}, "ram": []any{}, "storage": []any{}},
				},
			})
		default:
			acctest.NotFound(w, "no such route: "+r.URL.Path)
		}
	})
}

func TestAccDtcloudProjects(t *testing.T) {
	server := httptest.NewServer(fakeProjectAPI(t))
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
data "dtcloud_projects" "all" {}

data "dtcloud_project_quotas" "stg" {
  project_id = data.dtcloud_projects.all.active_project_id
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.dtcloud_projects.all", "projects.#", "2"),
					resource.TestCheckResourceAttr("data.dtcloud_projects.all", "projects.0.id", "proj-1"),
					resource.TestCheckResourceAttr("data.dtcloud_projects.all", "projects.0.domain_id", "dom-1"),

					// Which project and region the session is pointed at — the
					// thing everything this provider creates lands in.
					resource.TestCheckResourceAttr("data.dtcloud_projects.all", "active_project_id", "proj-2"),
					resource.TestCheckResourceAttr("data.dtcloud_projects.all", "active_project_name", "openstack-stg"),
					resource.TestCheckResourceAttr("data.dtcloud_projects.all", "active_region_id", "1"),

					// The quota data source fell back to the provider's region
					// for the path segment.
					resource.TestCheckResourceAttr("data.dtcloud_project_quotas.stg", "id", "1:proj-2"),
					resource.TestCheckResourceAttr("data.dtcloud_project_quotas.stg", "cpu.0.usage", "24"),
					resource.TestCheckResourceAttr("data.dtcloud_project_quotas.stg", "cpu.0.quota", "-1"),
					resource.TestCheckResourceAttr("data.dtcloud_project_quotas.stg", "ram.0.quota", "128"),
					resource.TestCheckResourceAttr("data.dtcloud_project_quotas.stg", "storage_space.#", "2"),
					resource.TestCheckResourceAttr("data.dtcloud_project_quotas.stg", "storage_space.0.name", "HI-IOPS"),
					resource.TestCheckResourceAttr("data.dtcloud_project_quotas.stg", "vm_status.0.count", "11"),
					resource.TestCheckResourceAttr("data.dtcloud_project_quotas.stg", "vm_status.0.running", "10"),
					resource.TestCheckResourceAttr("data.dtcloud_project_quotas.stg", "vm_status.0.stopped", "1"),
				),
			},
		},
	})
}
