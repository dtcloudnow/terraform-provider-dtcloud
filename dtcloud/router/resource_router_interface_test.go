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

// interfaceConfig attaches one network to a router. address is optional: with
// it the attach goes through the path that creates a port carrying exactly that
// address, without it through the path that takes the network's first subnet.
func interfaceConfig(endpoint, networkID, address string) string {
	ip := ""
	if address != "" {
		ip = fmt.Sprintf("  ip_address = %q\n", address)
	}
	return bareRouterConfig(endpoint, "tf-acc-router", "net-external", true) + fmt.Sprintf(`
resource "dtcloud_router_interface" "test" {
  router_id  = dtcloud_router.test.id
  network_id = %q
%s}

data "dtcloud_router_interfaces" "test" {
  router_id  = dtcloud_router.test.id
  depends_on = [dtcloud_router_interface.test]
}
`, networkID, ip)
}

// checkInterfacesDetached asserts the platform is left with none.
func checkInterfacesDetached(api *fakeRouterAPI) func(*terraform.State) error {
	return func(s *terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		for id, rt := range api.routers {
			if rt.deleted {
				continue
			}
			if len(rt.Interfaces) != 0 {
				return fmt.Errorf("router %s still has %d interfaces after destroy", id, len(rt.Interfaces))
			}
		}
		return nil
	}
}

// TestAccDtcloudRouterInterface_lifecycle drives attach → read → re-plan →
// import → detach.
//
// The data source reads the same listing the resource does, which is where the
// two kinds of entry sit side by side: the external gateway reports a subnet id
// in the field the internal interfaces use for a port id.
func TestAccDtcloudRouterInterface_lifecycle(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkInterfacesDetached(api),
		Steps: []resource.TestStep{
			{
				Config: interfaceConfig(server.URL, "net-private", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_router_interface.test", "network_id", "net-private"),
					resource.TestCheckResourceAttr("dtcloud_router_interface.test", "network_name", "private"),
					resource.TestCheckResourceAttr("dtcloud_router_interface.test", "subnet_id", "subnet-private"),
					resource.TestCheckResourceAttr("dtcloud_router_interface.test", "cidr", "10.0.10.0/24"),
					resource.TestCheckResourceAttrSet("dtcloud_router_interface.test", "port_id"),

					// Two entries: the gateway attached at create, and this
					// interface. The first carries a subnet id in `id` and no
					// network_id at all; the second carries the port id.
					resource.TestCheckResourceAttr("data.dtcloud_router_interfaces.test", "interfaces.#", "2"),
					resource.TestCheckResourceAttr("data.dtcloud_router_interfaces.test", "interfaces.0.type", "External gateway"),
					resource.TestCheckResourceAttr("data.dtcloud_router_interfaces.test", "interfaces.0.id", "subnet-external"),
					resource.TestCheckResourceAttr("data.dtcloud_router_interfaces.test", "interfaces.0.network_id", ""),
					resource.TestCheckResourceAttr("data.dtcloud_router_interfaces.test", "interfaces.1.type", "Internal interface"),
					resource.TestCheckResourceAttrPair(
						"data.dtcloud_router_interfaces.test", "interfaces.1.id",
						"dtcloud_router_interface.test", "port_id"),
				),
			},
			{
				Config:   interfaceConfig(server.URL, "net-private", ""),
				PlanOnly: true,
			},
			{
				ResourceName:      "dtcloud_router_interface.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccDtcloudRouterInterface_createWaitsForThePort is named after the rule
// it protects: the port id is discovered by watching the router's interface
// list, because the attach endpoint answers with the router instead of the port
// it created.
//
// The fake keeps a new interface out of the listing for several reads, so a
// create that took the first answer would end up with an empty port id — and an
// interface with no port id is one nothing can detach.
func TestAccDtcloudRouterInterface_createWaitsForThePort(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkInterfacesDetached(api),
		Steps: []resource.TestStep{
			{
				Config: interfaceConfig(server.URL, "net-private", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(s *terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						rt := theRouter(api)
						if rt == nil || len(rt.Interfaces) != 1 {
							return fmt.Errorf("expected exactly one interface on the platform")
						}
						want := rt.Interfaces[0].PortID
						got := s.RootModule().Resources["dtcloud_router_interface.test"].Primary.Attributes["port_id"]
						if got != want {
							return fmt.Errorf("port_id is %q, but the platform created port %q", got, want)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccDtcloudRouterInterface_requestedAddressIsSent is named after the rule
// it protects: asking for an address means sending the address without an IP
// version.
//
// The endpoint branches on the first fixed IP. An entry declaring IPv4 makes it
// attach the network's first subnet and **discard the address** — so a request
// built the obvious way, with both fields filled in, silently gets a different
// address from the one asked for. The fake reproduces that branch, so building
// the request the obvious way fails here.
func TestAccDtcloudRouterInterface_requestedAddressIsSent(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkInterfacesDetached(api),
		Steps: []resource.TestStep{
			{
				Config: interfaceConfig(server.URL, "net-private", "10.0.10.55"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_router_interface.test", "ip_address", "10.0.10.55"),
					func(s *terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						rt := theRouter(api)
						if rt == nil || len(rt.Interfaces) != 1 {
							return fmt.Errorf("expected exactly one interface on the platform")
						}
						if got := rt.Interfaces[0].IPAddress; got != "10.0.10.55" {
							return fmt.Errorf("the platform gave the interface %q; the requested address was discarded", got)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccDtcloudRouterInterface_detachWaitsForThePortToGo is named after the
// rule it protects: destroy does not return while the interface is still
// attached.
//
// The fake keeps a detached interface in the listing for several reads, so a
// delete that returned on the acknowledgement would let the router be destroyed
// underneath it.
func TestAccDtcloudRouterInterface_detachWaitsForThePortToGo(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: interfaceConfig(server.URL, "net-private", ""),
			},
			{
				// Destroying only the interface, so the check below runs
				// against a router that is still there to be inspected.
				Config:  bareRouterConfig(server.URL, "tf-acc-router", "net-external", true),
				Destroy: false,
				Check: func(s *terraform.State) error {
					api.mu.Lock()
					defer api.mu.Unlock()
					rt := theRouter(api)
					if rt == nil {
						return fmt.Errorf("the router is gone")
					}
					if len(rt.Interfaces) != 0 {
						return fmt.Errorf("terraform returned from the detach with %d interfaces still attached", len(rt.Interfaces))
					}
					return nil
				},
			},
		},
	})
}

// TestAccDtcloudRouterInterface_cannotDetachWhileARouteNeedsIt is named after
// the rule it protects: an interface a static route points through is pinned by
// that route, and the platform refuses to detach it.
//
// Found on a live run, by replacing an interface — an address change is
// ForceNew, so Terraform destroys the old one first — while a route pointed
// through it. The refusal is the platform's own and is passed through unchanged;
// it names the subnet and says why, which is more than the provider could add.
//
// The consequence for anyone using these resources is an ordering one: a
// router's routes have to go before its interfaces. Terraform only knows that
// if the configuration says so, which is why the example and both documents
// carry a `depends_on`.
func TestAccDtcloudRouterInterface_cannotDetachWhileARouteNeedsIt(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	// The next hop sits on the attached network, which is what pins the
	// interface. The route depends on the interface so that the teardown after
	// this test destroys them in the order the platform requires.
	withRoute := func(address string) string {
		return interfaceConfig(server.URL, "net-private", address) + `
resource "dtcloud_router_static_route" "pinning" {
  router_id   = dtcloud_router.test.id
  destination = "192.168.70.0/24"
  next_hop    = "10.0.10.20"

  depends_on = [dtcloud_router_interface.test]
}
`
	}

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: withRoute(""),
			},
			{
				// Changing the address replaces the interface, so this asks the
				// platform to detach one the route still needs.
				Config:      withRoute("10.0.10.5"),
				ExpectError: regexp.MustCompile(`required by one or more routes`),
			},
		},
	})
}
