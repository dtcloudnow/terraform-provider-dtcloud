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

// The three rules exercise three different shapes on the wire:
//
//	ssh    every field populated
//	ping   a port field set and its pair omitted — for ICMP those are type and
//	       code, not ports, so this is ordinary rather than exotic
//	open   protocol and both ports omitted, which reaches the provider as JSON
//	       nulls and must not read back as a difference
const rulesConfigTemplate = `
resource "dtcloud_security_group" "web" {
  name = "tf-acc-sg-rules"
}

resource "dtcloud_security_group" "peer" {
  name = "tf-acc-sg-peer"
}

resource "dtcloud_security_group_rule" "ssh" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 22
  port_range_max    = 22
  remote_ip_prefix  = "10.0.0.0/24"
}

%s

resource "dtcloud_security_group_rule" "open" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  remote_ip_prefix  = "0.0.0.0/0"
}

resource "dtcloud_security_group_rule" "from_peer" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 5432
  port_range_max    = 5432
  remote_group_id   = dtcloud_security_group.peer.id
}
`

const pingRule = `
resource "dtcloud_security_group_rule" "ping" {
  security_group_id = dtcloud_security_group.web.id
  direction         = "ingress"
  protocol          = "icmp"
  port_range_min    = 8
  remote_ip_prefix  = "0.0.0.0/0"
}
`

func rulesConfig(endpoint, extra string) string {
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(rulesConfigTemplate, extra)
}

// TestAccDtcloudSecurityGroupRule_lifecycle is the test the task is really
// about: removing one rule must leave the group and every other rule alone.
//
// It asserts that by remembering the surviving rules' ids across the step. A
// rule that had been destroyed and recreated would come back with a new id, and
// the group's own create count would move if the group had been rebuilt.
func TestAccDtcloudSecurityGroupRule_lifecycle(t *testing.T) {
	api := newFakeSecurityGroupAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	var sshID, openID, groupID string

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy: func(*terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			for id, r := range api.rules {
				if !r.deleted {
					return fmt.Errorf("rule %s still exists after destroy", id)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: rulesConfig(server.URL, pingRule),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_security_group_rule.ssh", "protocol", "tcp"),
					resource.TestCheckResourceAttr("dtcloud_security_group_rule.ssh", "port_range_min", "22"),
					// Omitted, so the platform chose it. Optional+Computed is
					// what stops that choice reading as a difference.
					resource.TestCheckResourceAttr("dtcloud_security_group_rule.ssh", "ethertype", "IPv4"),
					resource.TestCheckResourceAttr("dtcloud_security_group_rule.ssh", "normalized_cidr", "10.0.0.0/24"),

					// ICMP: min is the type, max was left out. It comes back as
					// JSON null and must settle as 0, not as a difference.
					resource.TestCheckResourceAttr("dtcloud_security_group_rule.ping", "port_range_min", "8"),
					resource.TestCheckResourceAttr("dtcloud_security_group_rule.ping", "port_range_max", "0"),

					// Protocol and both ports null.
					resource.TestCheckResourceAttr("dtcloud_security_group_rule.open", "protocol", ""),
					resource.TestCheckResourceAttr("dtcloud_security_group_rule.open", "port_range_min", "0"),
					resource.TestCheckResourceAttr("dtcloud_security_group_rule.open", "port_range_max", "0"),

					resource.TestCheckResourceAttrPair(
						"dtcloud_security_group_rule.from_peer", "remote_group_id",
						"dtcloud_security_group.peer", "id"),

					// Deliberately no assertion on the group's own rule list
					// here: it is a snapshot from that resource's last read,
					// which happened before these rules existed. Terraform
					// creates the group first, so within this apply it is
					// correctly empty. The next step, after a refresh, is where
					// it can be checked.
					resource.TestCheckResourceAttr("dtcloud_security_group.web", "inbound_rule.#", "0"),

					func(s *terraform.State) error {
						sshID = s.RootModule().Resources["dtcloud_security_group_rule.ssh"].Primary.ID
						openID = s.RootModule().Resources["dtcloud_security_group_rule.open"].Primary.ID
						groupID = s.RootModule().Resources["dtcloud_security_group.web"].Primary.ID

						api.mu.Lock()
						defer api.mu.Unlock()
						// Four rules plus the two the platform adds itself.
						if got := len(api.rulesOf(groupID)); got != 6 {
							return fmt.Errorf("expected 6 rules on the group, got %d", got)
						}
						return nil
					},
				),
			},
			{
				// The same configuration again. Nothing changes, but the
				// refresh re-reads the group — and now its rule snapshot is
				// populated, so the platform's display of these rules can be
				// checked: cooked protocol names, port ranges as strings, and a
				// referenced group rendered by *name* rather than id. None of
				// that could be turned back into a rule, which is why
				// dtcloud_security_group_rule reads the raw endpoint instead.
				Config: rulesConfig(server.URL, pingRule),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_security_group.web", "inbound_rule.#", "4"),
					resource.TestCheckTypeSetElemNestedAttrs("dtcloud_security_group.web", "inbound_rule.*",
						map[string]string{"protocol": "SSH", "port_range": "22", "source": "10.0.0.0/24"}),
					resource.TestCheckTypeSetElemNestedAttrs("dtcloud_security_group.web", "inbound_rule.*",
						map[string]string{"protocol": "ANY", "port_range": "1-65535", "source": "0.0.0.0/0"}),
					resource.TestCheckTypeSetElemNestedAttrs("dtcloud_security_group.web", "inbound_rule.*",
						map[string]string{"protocol": "TCP", "port_range": "5432", "source": "tf-acc-sg-peer"}),
					// ICMP with a type and no code: the platform renders the
					// pair as a range it cannot express and falls back to
					// "1-65535" even though this rule is not about ports at all.
					resource.TestCheckTypeSetElemNestedAttrs("dtcloud_security_group.web", "inbound_rule.*",
						map[string]string{"protocol": "ICMP", "port_range": "1-65535", "source": "0.0.0.0/0"}),
				),
			},
			{
				Config:   rulesConfig(server.URL, pingRule),
				PlanOnly: true,
			},
			{
				// Drop the ICMP rule. Nothing else may move.
				Config: rulesConfig(server.URL, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(s *terraform.State) error {
						if got := s.RootModule().Resources["dtcloud_security_group_rule.ssh"].Primary.ID; got != sshID {
							return fmt.Errorf("removing one rule recreated the ssh rule: id was %s, now %s", sshID, got)
						}
						if got := s.RootModule().Resources["dtcloud_security_group_rule.open"].Primary.ID; got != openID {
							return fmt.Errorf("removing one rule recreated the open rule: id was %s, now %s", openID, got)
						}

						api.mu.Lock()
						defer api.mu.Unlock()
						if api.creates != 2 {
							return fmt.Errorf("removing a rule must not touch the groups; %d group creates seen", api.creates)
						}
						if got := len(api.rulesOf(groupID)); got != 5 {
							return fmt.Errorf("expected 5 rules after removing one, got %d", got)
						}
						return nil
					},
				),
			},
			{
				ResourceName:      "dtcloud_security_group_rule.ssh",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccDtcloudSecurityGroupRule_planTimeRules pins the four things rejected
// before anything is sent. Each is something the platform would otherwise
// refuse mid-apply, after the plan had already looked fine.
func TestAccDtcloudSecurityGroupRule_planTimeRules(t *testing.T) {
	api := newFakeSecurityGroupAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	group := `
resource "dtcloud_security_group" "g" {
  name = "tf-acc-sg-plan-rules"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				// A port range needs a protocol: a rule matching every protocol
				// cannot also restrict ports.
				Config: acctest.ProviderConfig(server.URL) + group + `
resource "dtcloud_security_group_rule" "bad" {
  security_group_id = dtcloud_security_group.g.id
  direction         = "ingress"
  port_range_min    = 80
  port_range_max    = 80
}
`,
				ExpectError: regexp.MustCompile("protocol is required when a port range is given"),
			},
			{
				Config: acctest.ProviderConfig(server.URL) + group + `
resource "dtcloud_security_group_rule" "bad" {
  security_group_id = dtcloud_security_group.g.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 900
  port_range_max    = 100
}
`,
				ExpectError: regexp.MustCompile(`port_range_min \(900\) must not be greater than port_range_max \(100\)`),
			},
			{
				// An IPv4 prefix on an IPv6 rule.
				Config: acctest.ProviderConfig(server.URL) + group + `
resource "dtcloud_security_group_rule" "bad" {
  security_group_id = dtcloud_security_group.g.id
  direction         = "ingress"
  ethertype         = "IPv6"
  remote_ip_prefix  = "10.0.0.0/24"
}
`,
				ExpectError: regexp.MustCompile("is IPv4 but ethertype is IPv6"),
			},
			{
				// A rule scopes to a CIDR or to a group, never to both.
				Config: acctest.ProviderConfig(server.URL) + group + `
resource "dtcloud_security_group_rule" "bad" {
  security_group_id = dtcloud_security_group.g.id
  direction         = "ingress"
  remote_ip_prefix  = "10.0.0.0/24"
  remote_group_id   = "sg-9999"
}
`,
				ExpectError: regexp.MustCompile(`conflicts with`),
			},
		},
	})
}

// TestAccDtcloudSecurityGroupRule_icmpPortRangeIsNotComparedAsPorts guards a
// rule that is easy to get wrong later.
//
// For ICMP the two port fields are the message type and code, so a code below
// the type is ordinary — type 8 code 0 is a ping. An unconditional
// min <= max check would reject it. The check is therefore restricted to tcp
// and udp, and this pins that.
func TestAccDtcloudSecurityGroupRule_icmpTypeCodeOrder(t *testing.T) {
	api := newFakeSecurityGroupAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_security_group" "g" {
  name = "tf-acc-sg-icmp"
}

resource "dtcloud_security_group_rule" "echo" {
  security_group_id = dtcloud_security_group.g.id
  direction         = "ingress"
  protocol          = "icmp"
  port_range_min    = 8
  port_range_max    = 3
  remote_ip_prefix  = "0.0.0.0/0"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_security_group_rule.echo", "port_range_min", "8"),
					resource.TestCheckResourceAttr("dtcloud_security_group_rule.echo", "port_range_max", "3"),
				),
			},
		},
	})
}

// TestAccDtcloudSecurityGroupRule_duplicate covers the platform's own refusal,
// which the provider cannot anticipate at plan time and passes through.
func TestAccDtcloudSecurityGroupRule_duplicate(t *testing.T) {
	api := newFakeSecurityGroupAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_security_group" "g" {
  name = "tf-acc-sg-dup"
}

resource "dtcloud_security_group_rule" "a" {
  security_group_id = dtcloud_security_group.g.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 443
  port_range_max    = 443
  remote_ip_prefix  = "0.0.0.0/0"
}

resource "dtcloud_security_group_rule" "b" {
  security_group_id = dtcloud_security_group.g.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 443
  port_range_max    = 443
  remote_ip_prefix  = "0.0.0.0/0"

  depends_on = [dtcloud_security_group_rule.a]
}
`,
				ExpectError: regexp.MustCompile("SecurityGroupRuleExists"),
			},
		},
	})
}

// TestAccDtcloudSecurityGroupRule_badImportID checks a malformed composite id
// is rejected with a message that says what the right shape is, rather than
// producing a resource pointing at nothing.
func TestAccDtcloudSecurityGroupRule_badImportID(t *testing.T) {
	api := newFakeSecurityGroupAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_security_group" "g" {
  name = "tf-acc-sg-import"
}

resource "dtcloud_security_group_rule" "r" {
  security_group_id = dtcloud_security_group.g.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 8080
  port_range_max    = 8080
  remote_ip_prefix  = "0.0.0.0/0"
}
`,
			},
			{
				ResourceName:  "dtcloud_security_group_rule.r",
				ImportState:   true,
				ImportStateId: "not-a-pair",
				ExpectError:   regexp.MustCompile(`expected an id of the form <security-group-id>:<rule-id>`),
			},
		},
	})
}
