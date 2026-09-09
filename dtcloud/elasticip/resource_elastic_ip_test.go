package elasticip_test

import (
	"fmt"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// association renders the resource with whatever port block is passed in, so
// the same configuration can be allocated bare, pointed at a port, moved, and
// released.
func association(endpoint, extra string) string {
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(`
resource "dtcloud_elastic_ip" "test" {
  floating_network_id = "ext-net-1"
%s
}

data "dtcloud_elastic_ip" "by_address" {
  ip_address = dtcloud_elastic_ip.test.ip_address
}

data "dtcloud_elastic_ips" "all" {
  depends_on = [dtcloud_elastic_ip.test]
}
`, extra)
}

// TestAccDtcloudElasticIP_lifecycle is the ticket end to end: allocate,
// associate, move, disassociate, release — and, throughout, the address must
// never change, because the address is the whole point of the resource.
func TestAccDtcloudElasticIP_lifecycle(t *testing.T) {
	api := newFakeElasticIPAPI().
		withPort("port-web", "vm-web", "web-01", "VM", "10.0.0.11").
		withPort("port-api", "vm-api", "api-01", "VM", "10.0.0.12")
	server := httptest.NewServer(api)
	defer server.Close()

	var address, id string

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy: func(*terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			for id, ip := range api.ips {
				if !ip.deleted {
					return fmt.Errorf("elastic IP %s still allocated after destroy", id)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				// Allocated and pointed at nothing. DOWN is the resting state
				// here, not a failure — a waiter that insisted on ACTIVE would
				// hang forever on this perfectly normal address.
				Config: association(server.URL, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.test", "status", "DOWN"),
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.test", "port_id", ""),
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.test", "device_id", ""),
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.test", "router_id", ""),
					resource.TestCheckResourceAttrSet("dtcloud_elastic_ip.test", "ip_address"),

					resource.TestCheckResourceAttr("data.dtcloud_elastic_ip.by_address", "assigned_type", ""),
					resource.TestCheckResourceAttr("data.dtcloud_elastic_ip.by_address", "network_name", "Firewall_198_51_100_0"),
					resource.TestCheckResourceAttr("data.dtcloud_elastic_ips.all", "elastic_ips.#", "1"),

					func(s *terraform.State) error {
						rs := s.RootModule().Resources["dtcloud_elastic_ip.test"]
						address, id = rs.Primary.Attributes["ip_address"], rs.Primary.ID
						return nil
					},
				),
			},
			{
				Config:   association(server.URL, ""),
				PlanOnly: true,
			},
			{
				// Associate. The address must survive.
				Config: association(server.URL, `  port_id = "port-web"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.test", "status", "ACTIVE"),
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.test", "port_id", "port-web"),
					// Read out of the raw body: dt-go types port_details as a
					// string and the API sends an object, so the typed field is
					// nil exactly when it has something in it.
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.test", "device_id", "vm-web"),
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.test", "device_owner", "compute:nova"),
					// Not given, so the platform chose it.
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.test", "fixed_ip_address", "10.0.0.11"),
					resource.TestCheckResourceAttrSet("dtcloud_elastic_ip.test", "router_id"),

					resource.TestCheckResourceAttr("data.dtcloud_elastic_ip.by_address", "assigned_type", "VM"),
					resource.TestCheckResourceAttr("data.dtcloud_elastic_ip.by_address", "assigned_to", "web-01"),

					checkAddressSurvived(&address, &id),
				),
			},
			{
				// Move it to another machine. Still an update, still the same
				// address — releasing and re-allocating would hand back a
				// different one, which is the failure this resource exists to
				// avoid.
				Config: association(server.URL, `  port_id = "port-api"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.test", "port_id", "port-api"),
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.test", "device_id", "vm-api"),
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.test", "fixed_ip_address", "10.0.0.12"),
					resource.TestCheckResourceAttr("data.dtcloud_elastic_ip.by_address", "assigned_to", "api-01"),
					checkAddressSurvived(&address, &id),
				),
			},
			{
				// Disassociate by removing the argument. The endpoint has no
				// partial mode, so this has to go out as an empty object.
				Config: association(server.URL, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.test", "port_id", ""),
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.test", "status", "DOWN"),
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.test", "device_id", ""),
					checkAddressSurvived(&address, &id),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.creates != 1 {
							return fmt.Errorf("associating and disassociating must not re-allocate the address; %d allocations seen", api.creates)
						}
						if len(api.updates) == 0 {
							return fmt.Errorf("no update was sent")
						}
						last := api.updates[len(api.updates)-1]
						if v, ok := last["port_id"]; ok {
							return fmt.Errorf("the disassociate sent port_id=%v; it has to be absent, which is how the platform is told to detach", v)
						}
						return nil
					},
				),
			},
			{
				ResourceName:      "dtcloud_elastic_ip.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// checkAddressSurvived asserts the resource was updated rather than replaced.
// A replacement would allocate a different public address, which is the one
// thing that must never happen silently.
func checkAddressSurvived(address, id *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs := s.RootModule().Resources["dtcloud_elastic_ip.test"]
		if got := rs.Primary.Attributes["ip_address"]; got != *address {
			return fmt.Errorf("the public address changed from %s to %s; the resource was replaced when it should have been updated", *address, got)
		}
		if got := rs.Primary.ID; got != *id {
			return fmt.Errorf("the id changed from %s to %s; the resource was replaced", *id, got)
		}
		return nil
	}
}

// TestAccDtcloudElasticIP_associatedAtCreate covers allocating and associating
// in one call, which the create route supports and which skips the update path
// entirely.
func TestAccDtcloudElasticIP_associatedAtCreate(t *testing.T) {
	api := newFakeElasticIPAPI().withPort("port-lb", "lb-1", "public-lb", "LB", "10.0.0.50")
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_elastic_ip" "lb" {
  floating_network_id = "ext-net-1"
  port_id             = "port-lb"
  fixed_ip_address    = "10.0.0.50"
}

data "dtcloud_elastic_ips" "attached" {
  assigned_type = "LB"

  depends_on = [dtcloud_elastic_ip.lb]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.lb", "status", "ACTIVE"),
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.lb", "device_owner", "Octavia"),
					// A load balancer, not a VM — the address does not care, and
					// neither should the resource.
					resource.TestCheckResourceAttr("data.dtcloud_elastic_ips.attached", "elastic_ips.#", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_elastic_ips.attached", "elastic_ips.0.assigned_to", "public-lb"),
				),
			},
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_elastic_ip" "lb" {
  floating_network_id = "ext-net-1"
  port_id             = "port-lb"
  fixed_ip_address    = "10.0.0.50"
}

data "dtcloud_elastic_ips" "attached" {
  assigned_type = "LB"

  depends_on = [dtcloud_elastic_ip.lb]
}
`,
				PlanOnly: true,
			},
		},
	})
}

// TestAccDtcloudElasticIP_unusedFilter covers the question the plural data
// source is really for: which allocated addresses is nothing using. An idle
// address still costs quota and still bills.
func TestAccDtcloudElasticIP_unusedFilter(t *testing.T) {
	api := newFakeElasticIPAPI().withPort("port-web", "vm-web", "web-01", "VM", "10.0.0.11")
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_elastic_ip" "used" {
  floating_network_id = "ext-net-1"
  port_id             = "port-web"
}

resource "dtcloud_elastic_ip" "idle" {
  floating_network_id = "ext-net-1"
}

data "dtcloud_elastic_ips" "idle" {
  status = "DOWN"

  depends_on = [dtcloud_elastic_ip.used, dtcloud_elastic_ip.idle]
}

data "dtcloud_elastic_ips" "none_attached" {
  assigned_type = "none"

  depends_on = [dtcloud_elastic_ip.used, dtcloud_elastic_ip.idle]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.dtcloud_elastic_ips.idle", "elastic_ips.#", "1"),
					resource.TestCheckResourceAttrPair(
						"data.dtcloud_elastic_ips.idle", "ids.0", "dtcloud_elastic_ip.idle", "id"),
					// "none" is the only way to ask for the empty string the API
					// reports for an unattached address.
					resource.TestCheckResourceAttr("data.dtcloud_elastic_ips.none_attached", "elastic_ips.#", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_elastic_ips.none_attached", "elastic_ips.0.fixed_ip_address", ""),
				),
			},
		},
	})
}

// TestAccDtcloudElasticIP_fixedIPNeedsPort pins the one plan-time rule. Sent to
// the platform on its own, a fixed address is accepted and ignored, which is
// the worst of the three possible outcomes.
func TestAccDtcloudElasticIP_fixedIPNeedsPort(t *testing.T) {
	api := newFakeElasticIPAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_elastic_ip" "bad" {
  floating_network_id = "ext-net-1"
  fixed_ip_address    = "10.0.0.11"
}
`,
				ExpectError: regexp.MustCompile("fixed_ip_address needs port_id"),
			},
		},
	})
}

// TestAccDtcloudElasticIP_multiAddressPort covers a refusal that only shows up
// on real infrastructure: a port carrying more than one IPv4 address will not
// take a floating IP unless the request says which address to map to.
//
// Found on DEV, on a VM interface with four addresses. It is not something the
// provider can pre-empt — the port's addresses are not knowable at plan time —
// so the platform's message is what the user gets, and it is a good one. What
// this pins is that supplying fixed_ip_address resolves it.
func TestAccDtcloudElasticIP_multiAddressPort(t *testing.T) {
	api := newFakeElasticIPAPI().withMultiIPPort("port-many", "vm-many", "many-01", "193.168.1.25")
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_elastic_ip" "bad" {
  floating_network_id = "ext-net-1"
  port_id             = "port-many"
}
`,
				ExpectError: regexp.MustCompile("has multiple fixed IPv4 addresses"),
			},
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_elastic_ip" "good" {
  floating_network_id = "ext-net-1"
  port_id             = "port-many"
  fixed_ip_address    = "193.168.1.25"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.good", "status", "ACTIVE"),
					resource.TestCheckResourceAttr("dtcloud_elastic_ip.good", "fixed_ip_address", "193.168.1.25"),
				),
			},
		},
	})
}

// TestAccDtcloudElasticIP_releasedOutside covers the address someone released
// in the panel. The 404 arrives Neutron-shaped with no numeric code anywhere,
// so dterr.IsNotFound has only the message text to go on; if that stops
// matching, this becomes a hard error instead of a rebuild.
func TestAccDtcloudElasticIP_releasedOutside(t *testing.T) {
	api := newFakeElasticIPAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	config := acctest.ProviderConfig(server.URL) + `
resource "dtcloud_elastic_ip" "test" {
  floating_network_id = "ext-net-1"
}
`
	var id string

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(s *terraform.State) error {
					id = s.RootModule().Resources["dtcloud_elastic_ip.test"].Primary.ID
					return nil
				},
			},
			{
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()
					api.ips[id].deleted = true
				},
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccDtcloudElasticIP_quotaExhausted checks that the platform's refusal
// reaches the user rather than being swallowed. Floating IPs are quota-managed
// and billed, so this is a refusal people will actually meet.
func TestAccDtcloudElasticIP_quotaExhausted(t *testing.T) {
	api := newFakeElasticIPAPI()
	api.failCreate = true
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_elastic_ip" "over" {
  floating_network_id = "ext-net-1"
}
`,
				ExpectError: regexp.MustCompile("OverQuota|Quota exceeded"),
			},
		},
	})
}
