package router_test

import (
	"fmt"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

func staticRouteConfig(endpoint, destination, nextHop string) string {
	return bareRouterConfig(endpoint, "tf-acc-router", "net-external", true) + fmt.Sprintf(`
resource "dtcloud_router_static_route" "test" {
  router_id   = dtcloud_router.test.id
  destination = %q
  next_hop    = %q
}

data "dtcloud_router_static_routes" "test" {
  router_id  = dtcloud_router.test.id
  depends_on = [dtcloud_router_static_route.test]
}
`, destination, nextHop)
}

// checkRoutesGone asserts the router is left with none.
func checkRoutesGone(api *fakeRouterAPI) func(*terraform.State) error {
	return func(s *terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		for id, rt := range api.routers {
			if rt.deleted {
				continue
			}
			if len(rt.Routes) != 0 {
				return fmt.Errorf("router %s still carries %d static routes after destroy", id, len(rt.Routes))
			}
		}
		return nil
	}
}

// TestAccDtcloudRouterStaticRoute_lifecycle drives add → read → re-plan →
// import → remove.
//
// The data source reads the endpoint that renames both fields on the way out,
// so this also checks the provider gives them back under one set of names.
func TestAccDtcloudRouterStaticRoute_lifecycle(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkRoutesGone(api),
		Steps: []resource.TestStep{
			{
				Config: staticRouteConfig(server.URL, "192.168.50.0/24", "10.0.10.9"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_router_static_route.test", "destination", "192.168.50.0/24"),
					resource.TestCheckResourceAttr("dtcloud_router_static_route.test", "next_hop", "10.0.10.9"),
					resource.TestCheckResourceAttr("data.dtcloud_router_static_routes.test", "routes.#", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_router_static_routes.test", "routes.0.destination", "192.168.50.0/24"),
					resource.TestCheckResourceAttr("data.dtcloud_router_static_routes.test", "routes.0.next_hop", "10.0.10.9"),
				),
			},
			{
				Config:   staticRouteConfig(server.URL, "192.168.50.0/24", "10.0.10.9"),
				PlanOnly: true,
			},
			{
				ResourceName:      "dtcloud_router_static_route.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccDtcloudRouterStaticRoute_createWaitsUntilTheRouteIsListed is named
// after the rule it protects: create waits until the route appears on the
// router, rather than trusting the acknowledgement.
//
// The endpoint answers before the route is visible, and the endpoint next to it
// — the one that edits a route in place — answers 200 having changed nothing at
// all when the route it was given does not exist. On an API that can say yes
// and mean no, the only honest confirmation is reading the value back. The fake
// holds a new route out of the listing for several reads, so a create that
// skipped the wait would find nothing to read and lose the resource.
func TestAccDtcloudRouterStaticRoute_createWaitsUntilTheRouteIsListed(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkRoutesGone(api),
		Steps: []resource.TestStep{
			{
				Config: staticRouteConfig(server.URL, "192.168.60.0/24", "10.0.10.9"),
				Check: func(s *terraform.State) error {
					api.mu.Lock()
					defer api.mu.Unlock()
					rt := theRouter(api)
					if rt == nil {
						return fmt.Errorf("the router is gone")
					}
					if len(rt.Routes) != 1 {
						return fmt.Errorf("terraform returned from the add with %d routes on the platform, want 1", len(rt.Routes))
					}
					return nil
				},
			},
		},
	})
}

// TestAccDtcloudRouterStaticRoute_concurrentRoutesAllSurvive is named after the
// rule it protects: the routes of one router are applied one at a time.
//
// A route is added by reading the router's whole route list, appending to it
// and writing it back. Terraform applies up to ten resources at once, so three
// routes on one router read the same list before any of the writes land and the
// last write wins — two of the three would vanish. The provider serialises them
// on the router id; without that, this test loses routes.
func TestAccDtcloudRouterStaticRoute_concurrentRoutesAllSurvive(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	config := bareRouterConfig(server.URL, "tf-acc-router", "net-external", true) + `
resource "dtcloud_router_static_route" "a" {
  router_id   = dtcloud_router.test.id
  destination = "192.168.10.0/24"
  next_hop    = "10.0.10.9"
}

resource "dtcloud_router_static_route" "b" {
  router_id   = dtcloud_router.test.id
  destination = "192.168.20.0/24"
  next_hop    = "10.0.10.9"
}

resource "dtcloud_router_static_route" "c" {
  router_id   = dtcloud_router.test.id
  destination = "192.168.30.0/24"
  next_hop    = "10.0.10.9"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkRoutesGone(api),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(s *terraform.State) error {
					api.mu.Lock()
					defer api.mu.Unlock()
					rt := theRouter(api)
					if rt == nil {
						return fmt.Errorf("the router is gone")
					}
					if len(rt.Routes) != 3 {
						return fmt.Errorf("the platform carries %d of the 3 routes; the others were overwritten", len(rt.Routes))
					}
					return nil
				},
			},
		},
	})
}

// TestAccDtcloudRouterStaticRoute_rejectsMalformedEnds checks that the two
// opposite rules — a destination needs a prefix length, a next hop must not
// have one — are plan errors rather than requests rejected halfway through an
// apply.
func TestAccDtcloudRouterStaticRoute_rejectsMalformedEnds(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config:      staticRouteConfig(server.URL, "192.168.50.0", "10.0.10.9"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`expected .* to be a valid IPv4 Value|invalid CIDR`),
			},
			{
				Config:      staticRouteConfig(server.URL, "192.168.50.0/24", "10.0.10.9/32"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`expected .* to contain a valid IPv4 address`),
			},
		},
	})
}
