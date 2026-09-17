package loadbalancer_test

import (
	"fmt"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// lbConfig builds the whole tree in one config, which also proves the
// dependency ordering works.
func lbConfig(endpoint, lbName, algorithm string, interval int) string {
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(`
resource "dtcloud_lb" "test" {
  name           = %q
  flavor_id      = "lb-flavor"
  vip_network_id = "net-1"
  description    = "managed by terraform"
}

resource "dtcloud_lb_listener" "test" {
  lb_id         = dtcloud_lb.test.id
  protocol      = "HTTP"
  protocol_port = 80
  name          = "http-in"
}

resource "dtcloud_lb_pool" "test" {
  lb_id                 = dtcloud_lb.test.id
  listener_id           = dtcloud_lb_listener.test.listener_id
  backend_protocol      = "HTTP"
  backend_protocol_port = 8080
  lb_algorithm          = %q
}

resource "dtcloud_lb_health_monitor" "test" {
  lb_id               = dtcloud_lb.test.id
  pool_id             = dtcloud_lb_pool.test.pool_id
  type                = "HTTP"
  interval            = %d
  timeout             = 5
  healthy_threshold   = 2
  unhealthy_threshold = 3
  url_path            = "/healthz"
}

resource "dtcloud_lb_member" "test" {
  lb_id             = dtcloud_lb.test.id
  pool_id           = dtcloud_lb_pool.test.pool_id
  address           = "10.0.0.11"
  compute_server_id = "vm-1"
  protocol_port     = 8080
  name              = "web-01"
}

data "dtcloud_lb" "test" {
  id = dtcloud_lb.test.id
}

data "dtcloud_lb_vms" "candidates" {
  network_id = "net-1"
}
`, lbName, algorithm, interval)
}

// TestAccDtcloudLB_lifecycle drives the full tree: create → read → re-plan →
// in-place changes on the pool and the health monitor → import → destroy.
func TestAccDtcloudLB_lifecycle(t *testing.T) {
	api := newFakeLBAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy: func(*terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			for id, lb := range api.lbs {
				if !lb.deleted {
					return fmt.Errorf("load balancer %s still exists after destroy", id)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: lbConfig(server.URL, "tf-acc-lb", "ROUND_ROBIN", 10),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_lb.test", "name", "tf-acc-lb"),
					// Settling proves the create waiter polled: the fake reports "Creating"
					// on the first read, so reaching anything else means it looped.
					// "Active", not "ACTIVE" — the API answers with one folded word.
					resource.TestCheckResourceAttr("dtcloud_lb.test", "status", "Active"),
					resource.TestCheckResourceAttr("dtcloud_lb.test", "network.0.ip", "10.0.0.5"),

					resource.TestCheckResourceAttr("dtcloud_lb_listener.test", "protocol", "HTTP"),
					resource.TestCheckResourceAttr("dtcloud_lb_listener.test", "protocol_port", "80"),
					resource.TestCheckResourceAttrSet("dtcloud_lb_listener.test", "listener_id"),

					resource.TestCheckResourceAttr("dtcloud_lb_pool.test", "lb_algorithm", "ROUND_ROBIN"),
					resource.TestCheckResourceAttr("dtcloud_lb_pool.test", "backend_protocol_port", "8080"),

					resource.TestCheckResourceAttr("dtcloud_lb_health_monitor.test", "interval", "10"),
					resource.TestCheckResourceAttr("dtcloud_lb_health_monitor.test", "url_path", "/healthz"),

					resource.TestCheckResourceAttr("dtcloud_lb_member.test", "address", "10.0.0.11"),
					resource.TestCheckResourceAttrSet("dtcloud_lb_member.test", "member_id"),
					// The member was declared as "web-01" but the API reports the VM's
					// name. Both must survive: declared in `name`, reported in `vm_name`.
					resource.TestCheckResourceAttr("dtcloud_lb_member.test", "name", "web-01"),
					resource.TestCheckResourceAttr("dtcloud_lb_member.test", "vm_name", "web-01-vm"),

					// The pool's own state is a snapshot from before the monitor and the
					// member existed, so assert the tree against the API instead.
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						for _, lb := range api.lbs {
							if len(lb.listeners) != 1 || len(lb.pools) != 1 || len(lb.monitors) != 1 {
								return fmt.Errorf("expected 1 listener, 1 pool and 1 monitor; got %d/%d/%d",
									len(lb.listeners), len(lb.pools), len(lb.monitors))
							}
							if len(lb.pools[0].members) != 1 {
								return fmt.Errorf("expected 1 member in the pool, got %d", len(lb.pools[0].members))
							}
							if lb.pools[0].MonitorID == "" {
								return fmt.Errorf("the monitor was not attached to the pool")
							}
						}
						return nil
					},

					resource.TestCheckResourceAttr("data.dtcloud_lb.test", "name", "tf-acc-lb"),
					resource.TestCheckResourceAttr("data.dtcloud_lb_vms.candidates", "vms.#", "2"),
					resource.TestCheckResourceAttr("data.dtcloud_lb_vms.candidates", "vms.0.ip_address", "10.0.0.11"),
					// A VM can hold several addresses; ip_address is only the first.
					resource.TestCheckResourceAttr("data.dtcloud_lb_vms.candidates", "vms.1.ip_addresses.#", "2"),
					resource.TestCheckResourceAttr("data.dtcloud_lb_vms.candidates", "vms.1.ip_addresses.1", "10.0.0.13"),
				),
			},
			{
				// Re-applying the same config must be a no-op.
				Config:   lbConfig(server.URL, "tf-acc-lb", "ROUND_ROBIN", 10),
				PlanOnly: true,
			},
			{
				// Both of these are in-place updates; nothing may be recreated.
				Config: lbConfig(server.URL, "tf-acc-lb-renamed", "LEAST_CONNECTIONS", 20),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_lb.test", "name", "tf-acc-lb-renamed"),
					resource.TestCheckResourceAttr("dtcloud_lb_pool.test", "lb_algorithm", "LEAST_CONNECTIONS"),
					resource.TestCheckResourceAttr("dtcloud_lb_health_monitor.test", "interval", "20"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.creates != 1 {
							return fmt.Errorf("in-place updates must not recreate the load balancer; %d creates seen", api.creates)
						}
						return nil
					},
				),
			},
			{
				ResourceName:      "dtcloud_lb.test",
				ImportState:       true,
				ImportStateVerify: true,
				// Not echoed back: `enabled` and `network_type` are write-only, and
				// the VIP is reported only as the resolved network.
				ImportStateVerifyIgnore: []string{"enabled", "network_type", "vip_network_id", "vip_subnet_id", "vip_port_id"},
			},
			{
				ResourceName:      "dtcloud_lb_listener.test",
				ImportState:       true,
				ImportStateVerify: true,
				// Only what the list endpoint reports round-trips.
				ImportStateVerifyIgnore: []string{"insert_headers"},
			},
			{
				ResourceName:      "dtcloud_lb_pool.test",
				ImportState:       true,
				ImportStateVerify: true,
				// sticky_session is write-only: the pool list does not report it.
				ImportStateVerifyIgnore: []string{"sticky_session"},
			},
		},
	})
}

// TestAccDtcloudLB_requiresOneVipAnchor pins the ExactlyOneOf rule, so this is a
// plan-time error rather than a 400 from the server.
func TestAccDtcloudLB_requiresOneVipAnchor(t *testing.T) {
	api := newFakeLBAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_lb" "bad" {
  name           = "tf-acc-lb-bad"
  flavor_id      = "lb-flavor"
  vip_network_id = "net-1"
  vip_subnet_id  = "subnet-1"
}
`,
				ExpectError: regexp.MustCompile("only one of"),
			},
		},
	})
}

// TestAccDtcloudLBBalancingPool covers the composite shortcut resource.
func TestAccDtcloudLBBalancingPool(t *testing.T) {
	api := newFakeLBAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	config := acctest.ProviderConfig(server.URL) + `
resource "dtcloud_lb" "test" {
  name           = "tf-acc-lb-bp"
  flavor_id      = "lb-flavor"
  vip_network_id = "net-1"
}

resource "dtcloud_lb_balancing_pool" "test" {
  lb_id = dtcloud_lb.test.id

  lb_protocol  = "HTTP"
  lb_port      = 80
  lb_algorithm = "ROUND_ROBIN"

  sticky_session        = false
  backend_protocol      = "HTTP"
  backend_protocol_port = 8080

  type                = "HTTP"
  interval            = 10
  timeout             = 5
  healthy_threshold   = 2
  unhealthy_threshold = 3

  member {
    address           = "10.0.0.11"
    compute_server_id = "vm-1"
    protocol_port     = 8080
  }
}
`

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("dtcloud_lb_balancing_pool.test", "balancing_pool_id"),
					resource.TestCheckResourceAttr("dtcloud_lb_balancing_pool.test", "members_total", "1"),
					resource.TestCheckResourceAttrSet("dtcloud_lb_balancing_pool.test", "listener_id"),
					resource.TestCheckResourceAttrSet("dtcloud_lb_balancing_pool.test", "health_monitor_id"),
					resource.TestCheckResourceAttr("dtcloud_lb_balancing_pool.test", "status", "Active"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						for _, lb := range api.lbs {
							if len(lb.bps) != 1 {
								return fmt.Errorf("expected 1 balancing pool, got %d", len(lb.bps))
							}
							if lb.bps[0].MemberCount != 1 {
								return fmt.Errorf("expected the member to be created with the pool, got %d", lb.bps[0].MemberCount)
							}
						}
						return nil
					},
				),
			},
			{
				Config:   config,
				PlanOnly: true,
			},
			{
				// Every argument here is ForceNew, so anything the read cannot recover
				// shows up as a proposed rebuild. A member's port is reported by nothing.
				ResourceName:            "dtcloud_lb_balancing_pool.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"member"},
			},
		},
	})
}
