// Package securitygroup implements the dtcloud_security_group and
// dtcloud_security_group_rule resources and their data sources.
//
// # Why rules are their own resource
//
// A rule has its own lifecycle. The API has POST /rules and DELETE /rules/{id}
// and *no* update endpoint, so a rule is only ever created or destroyed —
// modelling rules as blocks on the group would mean the provider computing the
// difference between two sets and issuing creates and deletes to close it, and
// every rule change would read as a change to the group. As separate resources,
// adding or removing one rule disturbs neither the group nor the other rules.
// terraform-provider-openstack, over the same Neutron API, splits them the same
// way.
//
// # The two read endpoints disagree, on purpose
//
// This is the thing to understand before reading anything else here.
//
//	GET /securitygroups/{id}/details  ->  cooked, lossy, for display
//	GET /securitygroups/{id}/rules    ->  raw Neutron, round-trips
//
// `getSecurityGroupDetailsForClient` builds its rules for the web panel: it
// splits them into inbound/outbound, turns a protocol and port into a *display
// name* ("SSH", "ALL TCP", "ANY"), renders the port range as a string, and
// resolves a rule's source to the *name* of the security group it references.
// Worse, those display names are read out of the platform's `parameters` table,
// so 22 is "SSH" only until someone edits a row.
//
// None of that can be turned back into a rule. So the group resource and its
// data sources expose the cooked rules as a **read-only display snapshot**, and
// dtcloud_security_group_rule reads the raw endpoint, which reports the fields
// it was created with.
//
// # Everything here is synchronous
//
// Unlike networks and VMs, none of these routes calls socketUtils: Neutron
// answers when the work is done and the response is the final state. There are
// no waiters in this package, and that is deliberate rather than an omission.
package securitygroup

import (
	"fmt"
	"net"
	"regexp"
	"strings"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// Rule directions, as Neutron names them.
const (
	directionIngress = "ingress"
	directionEgress  = "egress"
)

// Address families, as Neutron spells them — capitalised, and rejected in any
// other casing by the API's Joi schema.
const (
	ethertypeIPv4 = "IPv4"
	ethertypeIPv6 = "IPv6"
)

// nameCharset is the API's own restriction on names and descriptions:
//
//	Joi.string().pattern(/^[^<>&"']*$/)
//
// Applying it here turns a 406 from the API into a plan-time error.
var nameCharset = regexp.MustCompile(`^[^<>&"']*$`)

const nameCharsetMessage = `must not contain <, >, &, ' or " — the API rejects them`

// cookedRuleSchema describes the display-only rules the details endpoint
// reports. It is shared by the resource and the singular data source.
//
// Everything in it is Computed. These are not the rules Terraform manages —
// those are dtcloud_security_group_rule resources — they are what the platform
// shows, reproduced so a plan or a `terraform show` says the same thing the web
// panel does.
func cookedRuleSchema(description string) *schema.Schema {
	return &schema.Schema{
		Type:        schema.TypeList,
		Computed:    true,
		Description: description,
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				"id": {
					Type:        schema.TypeString,
					Computed:    true,
					Description: "ID of the rule. This is the id half of a dtcloud_security_group_rule import.",
				},
				"protocol": {
					Type:     schema.TypeString,
					Computed: true,
					Description: "Display name the platform gives the rule's protocol, e.g. SSH, HTTPS, ALL TCP or ANY. " +
						"Presentation only — the names come from a configurable table and are not the protocol itself.",
				},
				"port_range": {
					Type:        schema.TypeString,
					Computed:    true,
					Description: "Port range as the platform renders it, e.g. \"443\", \"1-65535\" or \"-\" for ICMP.",
				},
				"source": {
					Type:     schema.TypeString,
					Computed: true,
					Description: "Where the rule applies to: a CIDR, or the *name* of the referenced security group " +
						"when the rule was written against one.",
				},
			},
		},
	}
}

// flattenCookedRules converts the details endpoint's rules for state.
func flattenCookedRules(rules []dtgo.Rule) []interface{} {
	out := make([]interface{}, 0, len(rules))
	for _, r := range rules {
		out = append(out, map[string]interface{}{
			"id":         r.ID,
			"protocol":   r.Protocol,
			"port_range": r.PortRange,
			"source":     r.Source,
		})
	}
	return out
}

// ruleID pairs the group and the rule, because Read finds a rule by listing the
// group's rules and a rule on its own cannot say which group to list. It is
// also the import format.
func ruleID(groupID, id string) string {
	return fmt.Sprintf("%s:%s", groupID, id)
}

// splitRuleID parses the form ruleID builds.
func splitRuleID(id string) (groupID, ruleID string, err error) {
	parts := strings.Split(id, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected an id of the form <security-group-id>:<rule-id>, got %q", id)
	}
	return parts[0], parts[1], nil
}

// cidrFamily reports the address family of a CIDR as an ethertype, or "" when
// it does not parse. Used to catch an IPv4 prefix on an IPv6 rule at plan time.
func cidrFamily(cidr string) string {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return ""
	}
	if ip.To4() != nil {
		return ethertypeIPv4
	}
	return ethertypeIPv6
}
