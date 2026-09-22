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

// interfaceConfig attaches one network to a router. With an address the attach
// goes through the path that creates a port carrying it; without one, through
// the path that takes the network's first subnet.
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
// import → detach. The data source reads the same listing, where the two kinds
// of entry sit side by side.
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

					// Two entries: the gateway attached at create, carrying a subnet id and no
					// network_id, and this interface, carrying the port id.
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

// TestAccDtcloudRouterInterface_createWaitsForThePort pins that the port id is
// found by watching the interface list, because the attach endpoint answers with
// the router. The fake holds a new interface out of the listing for several
// reads, so a create that took the first answer would end up with no port id.
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

// TestAccDtcloudRouterInterface_requestedAddressIsSent pins that asking for an
// address means sending it without an IP version. The endpoint branches on the
// first fixed IP and an entry declaring IPv4 discards the address, so a request
// built the obvious way silently gets a different one.
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

// TestAccDtcloudRouterInterface_detachWaitsForThePortToGo pins that destroy does
// not return while the interface is still attached. The fake keeps a detached
// one in the listing for several reads.
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

// TestAccDtcloudRouterInterface_cannotDetachWhileARouteNeedsIt pins that an
// interface a static route points through is refused by the platform. Found on a
// live run by replacing an interface while a route pointed through it.
//
// The consequence is an ordering one: a router's routes have to go before its
// interfaces, and Terraform only knows that if the configuration says so — which
// is why the example and both documents carry a `depends_on`.
func TestAccDtcloudRouterInterface_cannotDetachWhileARouteNeedsIt(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	// The next hop sits on the attached network, which is what pins the
	// interface. The depends_on makes the teardown destroy them in order.
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
				// Changing the address replaces the interface, so this asks the platform
				// to detach one the route still needs.
				Config:      withRoute("10.0.10.5"),
				ExpectError: regexp.MustCompile(`required by one or more routes`),
			},
		},
	})
}

// TestAccDtcloudRouterInterface_concurrentAttachesGetTheirOwnPort is named after
// the rule it protects: two interfaces attached to one router at the same time
// each end up holding the port that belongs to it.
//
// The attach endpoint answers with the router rather than the port it created,
// so the new port is found by comparing the router's interface list before and
// after. Terraform applies up to ten resources at once, so two attaches overlap
// and either comparison can see the other's port. The provider serialises them
// on the router id; without that, both resources can adopt the same one.
func TestAccDtcloudRouterInterface_concurrentAttachesGetTheirOwnPort(t *testing.T) {
	api := newFakeRouterAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	config := bareRouterConfig(server.URL, "tf-acc-router", "net-external", true) + `
resource "dtcloud_router_interface" "a" {
  router_id  = dtcloud_router.test.id
  network_id = "net-private"
}

resource "dtcloud_router_interface" "b" {
  router_id  = dtcloud_router.test.id
  network_id = "net-private2"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkInterfacesDetached(api),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(s *terraform.State) error {
					a := s.RootModule().Resources["dtcloud_router_interface.a"]
					b := s.RootModule().Resources["dtcloud_router_interface.b"]
					if a == nil || b == nil {
						return fmt.Errorf("both interfaces should be in state")
					}
					pa, pb := a.Primary.Attributes["port_id"], b.Primary.Attributes["port_id"]
					if pa == "" || pb == "" {
						return fmt.Errorf("port_id is empty: a=%q b=%q", pa, pb)
					}
					if pa == pb {
						return fmt.Errorf("both interfaces adopted port %s; one of them is holding the other's", pa)
					}

					api.mu.Lock()
					defer api.mu.Unlock()
					rt := theRouter(api)
					if rt == nil {
						return fmt.Errorf("the router is gone")
					}
					if len(rt.Interfaces) != 2 {
						return fmt.Errorf("the platform carries %d of the 2 interfaces", len(rt.Interfaces))
					}
					return nil
				},
			},
		},
	})
}
