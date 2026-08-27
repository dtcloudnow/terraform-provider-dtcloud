package network_test

import (
	"fmt"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

func networkConfig(endpoint, name, gateway string, dhcp bool, dns string) string {
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(`
resource "dtcloud_network" "test" {
  name        = %q
  cidr        = "10.20.30.0/24"
  gateway_ip  = %q
  enable_dhcp = %t

  dns_nameservers = %s

  allocation_pools {
    start = "10.20.30.10"
    end   = "10.20.30.200"
  }
}

data "dtcloud_network" "test" {
  id = dtcloud_network.test.id
}

data "dtcloud_networks" "all" {
  depends_on = [dtcloud_network.test]
}
`, name, gateway, dhcp, dns)
}

// TestAccDtcloudNetwork_lifecycle drives create → read → re-plan → in-place
// rename and subnet edit → import → destroy.
func TestAccDtcloudNetwork_lifecycle(t *testing.T) {
	api := newFakeNetworkAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy: func(*terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			for id, n := range api.networks {
				if !n.deleted {
					return fmt.Errorf("network %s still exists after destroy", id)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: networkConfig(server.URL, "tf-acc-net", "10.20.30.1", true, `["8.8.8.8"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_network.test", "name", "tf-acc-net"),
					resource.TestCheckResourceAttr("dtcloud_network.test", "cidr", "10.20.30.0/24"),
					resource.TestCheckResourceAttr("dtcloud_network.test", "gateway_ip", "10.20.30.1"),
					resource.TestCheckResourceAttr("dtcloud_network.test", "enable_dhcp", "true"),
					resource.TestCheckResourceAttr("dtcloud_network.test", "ipam_enabled", "true"),
					resource.TestCheckResourceAttr("dtcloud_network.test", "network_type", "Virtual"),
					resource.TestCheckResourceAttr("dtcloud_network.test", "ip_version", "4"),
					resource.TestCheckResourceAttr("dtcloud_network.test", "dns_nameservers.0", "8.8.8.8"),
					resource.TestCheckResourceAttr("dtcloud_network.test", "allocation_pools.0.start", "10.20.30.10"),
					// The id has to survive being buried in the createSubnet
					// response as network_id — the create call never returns it
					// at the top level when IPAM is on.
					resource.TestCheckResourceAttrSet("dtcloud_network.test", "subnet_id"),

					resource.TestCheckResourceAttr("data.dtcloud_network.test", "name", "tf-acc-net"),
					resource.TestCheckResourceAttr("data.dtcloud_network.test", "ipam_enabled", "true"),
					resource.TestCheckResourceAttr("data.dtcloud_networks.all", "networks.#", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_networks.all", "networks.0.ipam", "Enabled"),
					resource.TestCheckResourceAttr("data.dtcloud_networks.all", "networks.0.dhcp", "Enabled"),
				),
			},
			{
				// Re-applying the same config must be a no-op.
				Config:   networkConfig(server.URL, "tf-acc-net", "10.20.30.1", true, `["8.8.8.8"]`),
				PlanOnly: true,
			},
			{
				// A rename and a subnet edit together: both are in-place, and
				// the network must not be recreated.
				Config: networkConfig(server.URL, "tf-acc-net-renamed", "10.20.30.254", false, `["1.1.1.1", "9.9.9.9"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_network.test", "name", "tf-acc-net-renamed"),
					resource.TestCheckResourceAttr("dtcloud_network.test", "gateway_ip", "10.20.30.254"),
					resource.TestCheckResourceAttr("dtcloud_network.test", "enable_dhcp", "false"),
					resource.TestCheckResourceAttr("dtcloud_network.test", "dns_nameservers.#", "2"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.creates != 1 {
							return fmt.Errorf("in-place updates must not recreate the network; %d creates seen", api.creates)
						}
						// The subnet endpoint takes the whole set or nothing, so
						// a partial patch would break the next unrelated edit.
						if len(api.subnetUpdates) == 0 {
							return fmt.Errorf("no subnet update was sent")
						}
						last := api.subnetUpdates[len(api.subnetUpdates)-1]
						for _, field := range []string{"enable_dhcp", "dns_nameservers", "allocation_pools"} {
							if _, ok := last[field]; !ok {
								return fmt.Errorf("subnet update left out %q; the endpoint requires the complete set", field)
							}
						}
						return nil
					},
				),
			},
			{
				ResourceName:      "dtcloud_network.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccDtcloudNetwork_withoutIPAM covers the other half of the resource: no
// subnet, and a create response shaped completely differently.
func TestAccDtcloudNetwork_withoutIPAM(t *testing.T) {
	api := newFakeNetworkAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	config := acctest.ProviderConfig(server.URL) + `
resource "dtcloud_network" "plain" {
  name         = "tf-acc-net-noipam"
  ipam_enabled = false
}
`

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_network.plain", "ipam_enabled", "false"),
					resource.TestCheckResourceAttr("dtcloud_network.plain", "subnet_id", ""),
					// Not empty — absent. cidr is Optional with no Computed, so
					// with no subnet to read one from it never enters state.
					resource.TestCheckNoResourceAttr("dtcloud_network.plain", "cidr"),
					// The id came from a bare network object with no wrapper.
					resource.TestCheckResourceAttrSet("dtcloud_network.plain", "id"),
				),
			},
			{
				Config:   config,
				PlanOnly: true,
			},
			{
				// The import that caught a real bug live: enable_dhcp has a
				// schema default of true and no subnet to read it from, so a
				// read that left it alone imported false, disagreed with the
				// configuration, and the resulting "update" reached the subnet
				// endpoint with no subnet to update.
				ResourceName:      "dtcloud_network.plain",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccDtcloudNetwork_ipamRules pins the two rules CustomizeDiff enforces.
// Both would otherwise be a 500 from the API after the plan looked fine.
func TestAccDtcloudNetwork_ipamRules(t *testing.T) {
	api := newFakeNetworkAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_network" "bad" {
  name = "tf-acc-net-nocidr"
}
`,
				ExpectError: regexp.MustCompile("cidr is required when ipam_enabled is true"),
			},
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_network" "bad" {
  name         = "tf-acc-net-conflict"
  ipam_enabled = false
  cidr         = "10.0.0.0/24"
}
`,
				ExpectError: regexp.MustCompile("cidr must not be set when ipam_enabled is false"),
			},
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_network" "bad" {
  name         = "tf-acc-net-conflict2"
  ipam_enabled = false
  gateway_ip   = "10.0.0.1"
}
`,
				ExpectError: regexp.MustCompile("gateway_ip must not be set when ipam_enabled is false"),
			},
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_network" "bad" {
  name         = "tf-acc-net-conflict3"
  ipam_enabled = false
  enable_dhcp  = false
}
`,
				ExpectError: regexp.MustCompile("enable_dhcp must not be set when ipam_enabled is false"),
			},
		},
	})
}
