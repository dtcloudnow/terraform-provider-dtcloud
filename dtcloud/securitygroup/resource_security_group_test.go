package securitygroup_test

import (
	"fmt"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

func groupConfig(endpoint, name, description string) string {
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(`
resource "dtcloud_security_group" "test" {
  name        = %q
  description = %q
}

data "dtcloud_security_group" "by_id" {
  id = dtcloud_security_group.test.id
}

data "dtcloud_security_group" "by_name" {
  name = dtcloud_security_group.test.name
}

data "dtcloud_security_groups" "all" {
  depends_on = [dtcloud_security_group.test]
}
`, name, description)
}

// TestAccDtcloudSecurityGroup_lifecycle drives create → read → re-plan →
// in-place edit → import → destroy, and covers both data sources alongside.
func TestAccDtcloudSecurityGroup_lifecycle(t *testing.T) {
	api := newFakeSecurityGroupAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy: func(*terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			for id, g := range api.groups {
				if !g.deleted {
					return fmt.Errorf("security group %s still exists after destroy", id)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: groupConfig(server.URL, "tf-acc-sg", "web tier"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_security_group.test", "name", "tf-acc-sg"),
					resource.TestCheckResourceAttr("dtcloud_security_group.test", "description", "web tier"),

					// A brand-new group is not empty: allow-all egress is added for each
					// address family. They belong to nobody, and the snapshot has to show them.
					resource.TestCheckResourceAttr("dtcloud_security_group.test", "outbound_rule.#", "2"),
					resource.TestCheckResourceAttr("dtcloud_security_group.test", "outbound_rule.0.protocol", "ANY"),
					resource.TestCheckResourceAttr("dtcloud_security_group.test", "outbound_rule.0.port_range", "1-65535"),
					resource.TestCheckResourceAttr("dtcloud_security_group.test", "outbound_rule.0.source", "0.0.0.0/0"),
					resource.TestCheckResourceAttr("dtcloud_security_group.test", "outbound_rule.1.source", "::/0"),
					resource.TestCheckResourceAttr("dtcloud_security_group.test", "inbound_rule.#", "0"),

					resource.TestCheckResourceAttrPair(
						"data.dtcloud_security_group.by_id", "id", "dtcloud_security_group.test", "id"),
					resource.TestCheckResourceAttr("data.dtcloud_security_group.by_id", "description", "web tier"),
					// The name lookup is the one that saves a hand-copied uuid.
					resource.TestCheckResourceAttrPair(
						"data.dtcloud_security_group.by_name", "id", "dtcloud_security_group.test", "id"),

					resource.TestCheckResourceAttr("data.dtcloud_security_groups.all", "security_groups.#", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_security_groups.all", "ids.#", "1"),
					resource.TestCheckResourceAttrPair(
						"data.dtcloud_security_groups.all", "ids.0", "dtcloud_security_group.test", "id"),
				),
			},
			{
				// Re-applying the same configuration must do nothing.
				Config:   groupConfig(server.URL, "tf-acc-sg", "web tier"),
				PlanOnly: true,
			},
			{
				Config: groupConfig(server.URL, "tf-acc-sg-renamed", "web and api tier"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_security_group.test", "name", "tf-acc-sg-renamed"),
					resource.TestCheckResourceAttr("dtcloud_security_group.test", "description", "web and api tier"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.creates != 1 {
							return fmt.Errorf("an in-place edit must not recreate the group; %d creates seen", api.creates)
						}
						if len(api.groupUpdates) == 0 {
							return fmt.Errorf("no update was sent")
						}
						// The update route requires a name, so it has to go out even on an
						// edit that only touched the description.
						last := api.groupUpdates[len(api.groupUpdates)-1]
						if _, ok := last["name"]; !ok {
							return fmt.Errorf("update left out %q, which the endpoint requires", "name")
						}
						return nil
					},
				),
			},
			{
				ResourceName:      "dtcloud_security_group.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccDtcloudSecurityGroup_descriptionCanBeCleared pins the round trip that
// removes a description. It is sent as a pointer, so an empty one arrives as
// `description: ""` and is read as "clear it"; dropping the empty value would
// leave the old description in place and the plan proposing it forever.
func TestAccDtcloudSecurityGroup_descriptionCanBeCleared(t *testing.T) {
	api := newFakeSecurityGroupAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_security_group" "test" {
  name        = "tf-acc-sg-desc"
  description = "will go away"
}
`,
				Check: resource.TestCheckResourceAttr("dtcloud_security_group.test", "description", "will go away"),
			},
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_security_group" "test" {
  name = "tf-acc-sg-desc"
}
`,
				Check: resource.TestCheckResourceAttr("dtcloud_security_group.test", "description", ""),
			},
		},
	})
}

// TestAccDtcloudSecurityGroup_nameCharset pins the API's character rule as
// a plan-time error rather than a 406 halfway through an apply.
func TestAccDtcloudSecurityGroup_nameCharset(t *testing.T) {
	api := newFakeSecurityGroupAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_security_group" "bad" {
  name = "tf-acc-<script>"
}
`,
				ExpectError: regexp.MustCompile(`must not contain`),
			},
		},
	})
}

// TestAccDtcloudSecurityGroup_deletedOutside is the test that earns the fake's
// error shape. These routes carry no numeric code anywhere, so dterr.IsNotFound
// has to recognise "does not exist" in the message; if that fallback stops
// working, a group deleted elsewhere becomes a hard error instead of a rebuild.
func TestAccDtcloudSecurityGroup_deletedOutside(t *testing.T) {
	api := newFakeSecurityGroupAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	config := acctest.ProviderConfig(server.URL) + `
resource "dtcloud_security_group" "test" {
  name = "tf-acc-sg-vanishing"
}
`

	var groupID string

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(s *terraform.State) error {
					rs, ok := s.RootModule().Resources["dtcloud_security_group.test"]
					if !ok {
						return fmt.Errorf("resource not in state")
					}
					groupID = rs.Primary.ID
					return nil
				},
			},
			{
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()
					api.groups[groupID].deleted = true
				},
				Config: config,
				// The refresh 404s, the resource drops out of state, and the plan
				// proposes to build it again.
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccDtcloudSecurityGroup_inUse covers the delete the platform refuses: a
// group still bound to a port cannot go, and that has to surface rather than be
// swallowed as "already gone".
func TestAccDtcloudSecurityGroup_inUse(t *testing.T) {
	api := newFakeSecurityGroupAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	config := acctest.ProviderConfig(server.URL) + `
resource "dtcloud_security_group" "test" {
  name = "tf-acc-sg-in-use"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(s *terraform.State) error {
					rs := s.RootModule().Resources["dtcloud_security_group.test"]
					api.mu.Lock()
					defer api.mu.Unlock()
					api.inUse[rs.Primary.ID] = true
					return nil
				},
			},
			{
				Config:      config,
				Destroy:     true,
				ExpectError: regexp.MustCompile("SecurityGroupInUse"),
			},
			{
				// Release it again, so the framework's own final destroy has something
				// it can actually delete.
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()
					api.inUse = map[string]bool{}
				},
				Config: config,
			},
		},
	})
}

// TestAccDtcloudMyIP covers the data source that scopes a rule to the caller
// without anyone looking their address up by hand.
func TestAccDtcloudMyIP(t *testing.T) {
	api := newFakeSecurityGroupAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
data "dtcloud_my_ip" "current" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.dtcloud_my_ip.current", "ip", "203.0.113.7"),
					// The whole point of the attribute: remote_ip_prefix wants a CIDR.
					resource.TestCheckResourceAttr("data.dtcloud_my_ip.current", "cidr", "203.0.113.7/32"),
				),
			},
		},
	})
}
