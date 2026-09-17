package router_test

import (
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// checkTerraformOwnedGone asserts everything this provider created is gone.
// Seeded routers are skipped by name — Terraform does not own them.
func checkTerraformOwnedGone(api *fakeRouterAPI) func(*terraform.State) error {
	return func(*terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		for id, rt := range api.routers {
			if !strings.HasPrefix(rt.Name, "tf-acc-") {
				continue
			}
			if !rt.deleted {
				return fmt.Errorf("router %s (%s) still exists after destroy", id, rt.Name)
			}
		}
		return nil
	}
}

// theRouter returns the single router the fake holds, for checks that need to
// look at the platform rather than at Terraform's state.
func theRouter(api *fakeRouterAPI) *fakeRouter {
	for _, rt := range api.routers {
		if !rt.deleted {
			return rt
		}
	}
	return nil
}

// bareRouterConfig is the resource on its own.
func bareRouterConfig(endpoint, name, networkID string, snat bool) string {
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(`
resource "dtcloud_router" "test" {
  name                = %q
  external_network_id = %q
  enable_snat         = %t
}
`, name, networkID, snat)
}

// routerConfig adds the data sources, so a lifecycle run exercises both the
// details endpoint and the listing, which describe the same gateway differently.
func routerConfig(endpoint, name, networkID string, snat bool) string {
	return bareRouterConfig(endpoint, name, networkID, snat) + `
data "dtcloud_router" "test" {
  id = dtcloud_router.test.id
}

data "dtcloud_routers" "all" {
  depends_on = [dtcloud_router.test]
}
`
}

// TestAccDtcloudRouter_lifecycle drives create → read → re-plan → import →
// destroy, with both data sources reading along.
func TestAccDtcloudRouter_lifecycle(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: routerConfig(server.URL, "tf-acc-router", "net-external", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_router.test", "name", "tf-acc-router"),
					resource.TestCheckResourceAttr("dtcloud_router.test", "external_network_id", "net-external"),
					resource.TestCheckResourceAttr("dtcloud_router.test", "enable_snat", "true"),
					resource.TestCheckResourceAttr("dtcloud_router.test", "admin_state_up", "true"),
					resource.TestCheckResourceAttr("dtcloud_router.test", "project_id", "project-0001"),
					// The gateway's address is chosen by the platform and only reported back.
					resource.TestCheckResourceAttr("dtcloud_router.test", "external_fixed_ip.#", "1"),
					resource.TestCheckResourceAttr("dtcloud_router.test", "external_fixed_ip.0.ip_address", "203.0.113.42"),
					resource.TestCheckResourceAttr("dtcloud_router.test", "external_fixed_ip.0.subnet_id", "subnet-external"),
					// created_at is declared ahead of project_id, so a project id proves the
					// fields behind the timestamp survived decoding.
					resource.TestCheckResourceAttr("dtcloud_router.test", "created_at", fakeRouterTime),
					// Never null here: the platform stamps it while a new router settles.
					resource.TestCheckResourceAttr("dtcloud_router.test", "updated_at", fakeRouterTime),

					// The singular data source reads details, so it reports the network by id.
					resource.TestCheckResourceAttr("data.dtcloud_router.test", "external_network_id", "net-external"),
					resource.TestCheckResourceAttr("data.dtcloud_router.test", "name", "tf-acc-router"),

					// The listing reads a different endpoint, which reports the same network by
					// name and adds its CIDR. Neither value is invented from the other.
					resource.TestCheckResourceAttr("data.dtcloud_routers.all", "routers.#", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_routers.all", "routers.0.external_network", "public"),
					resource.TestCheckResourceAttr("data.dtcloud_routers.all", "routers.0.cidr", "203.0.113.0/24"),
					resource.TestCheckResourceAttr("data.dtcloud_routers.all", "routers.0.is_external", "true"),
					resource.TestCheckResourceAttr("data.dtcloud_routers.all", "routers.0.enable_snat", "true"),
				),
			},
			{
				Config:   routerConfig(server.URL, "tf-acc-router", "net-external", true),
				PlanOnly: true,
			},
			{
				ResourceName:      "dtcloud_router.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccDtcloudRouter_createWaitsForActive pins that create does not return
// until the platform reports ACTIVE. Nothing here updates, so no second wait
// could settle the status behind this one.
func TestAccDtcloudRouter_createWaitsForActive(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: bareRouterConfig(server.URL, "tf-acc-active", "net-external", true),
				Check:  resource.TestCheckResourceAttr("dtcloud_router.test", "status", "ACTIVE"),
			},
		},
	})
}

// TestAccDtcloudRouter_renameWaitsForTheName pins that the update wait watches
// the name, not the status: a rename is acknowledged while the router is still
// ACTIVE and still reporting the old one. It has a step to itself so no other
// wait can settle the name behind it.
func TestAccDtcloudRouter_renameWaitsForTheName(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: bareRouterConfig(server.URL, "tf-acc-router", "net-external", true),
				Check:  resource.TestCheckResourceAttr("dtcloud_router.test", "name", "tf-acc-router"),
			},
			{
				Config: bareRouterConfig(server.URL, "tf-acc-router-renamed", "net-external", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_router.test", "name", "tf-acc-router-renamed"),
					// Renaming is an update, not a replacement.
					resource.TestCheckResourceAttr("dtcloud_router.test", "enable_snat", "true"),
				),
			},
		},
	})
}

// TestAccDtcloudRouter_gatewayUpdateWaitsForTheValue is the same rule for the
// other half. SNAT and the external network change one step at a time, so
// neither wait can be deleted without something noticing.
func TestAccDtcloudRouter_gatewayUpdateWaitsForTheValue(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: bareRouterConfig(server.URL, "tf-acc-router", "net-external", true),
				Check:  resource.TestCheckResourceAttr("dtcloud_router.test", "enable_snat", "true"),
			},
			{
				Config: bareRouterConfig(server.URL, "tf-acc-router", "net-external", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_router.test", "enable_snat", "false"),
					resource.TestCheckResourceAttr("dtcloud_router.test", "external_network_id", "net-external"),
				),
			},
			{
				Config: bareRouterConfig(server.URL, "tf-acc-router", "net-external2", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_router.test", "external_network_id", "net-external2"),
					// Moving the gateway is an in-place change: the router that
					// comes out is the one that went in.
					func(s *terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.creates != 1 {
							return fmt.Errorf("expected the router to be updated in place, but it was created %d times", api.creates)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccDtcloudRouter_recreatedWhenDeletedOutsideTerraform covers the other
// half of the 404 rule: a router that is gone drops out of state and the next
// apply builds it again. It matters here because these endpoints answer with no
// status code at all — see TestRouterNotFoundIsClassified.
func TestAccDtcloudRouter_recreatedWhenDeletedOutsideTerraform(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	config := bareRouterConfig(server.URL, "tf-acc-gone", "net-external", true)

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()
					theRouter(api).deleted = true
				},
				Config: config,
				Check: func(s *terraform.State) error {
					api.mu.Lock()
					defer api.mu.Unlock()
					if api.creates != 2 {
						return fmt.Errorf("expected the router to be created again, got %d creates", api.creates)
					}
					return nil
				},
			},
		},
	})
}

// TestAccDtcloudRouter_rejectsNameWithHTML checks the character rule is a plan
// error rather than a request rejected halfway through an apply.
func TestAccDtcloudRouter_rejectsNameWithHTML(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config:      bareRouterConfig(server.URL, "tf-acc-<script>", "net-external", true),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`must not contain`),
			},
		},
	})
}
