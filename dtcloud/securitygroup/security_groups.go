// Package securitygroup implements the dtcloud_security_group and
// dtcloud_security_group_rule resources and their data sources.
//
// Rules are a separate resource: they are only ever created or destroyed, and as
// blocks on the group every rule change would read as a change to the group.
//
// The two read endpoints disagree. The details endpoint renders rules for
// display and none of it converts back into a rule, so the group exposes them as
// a read-only snapshot and the rule resource reads the raw endpoint.
//
// Every route here is synchronous, so there are no waiters.
package securitygroup

import (
	"fmt"
	"net"
	"regexp"
	"strings"

	dtgo "github.com/dtcloudnow/dt-go"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// Rule directions.
const (
	directionIngress = "ingress"
	directionEgress  = "egress"
)

// Address families — capitalised, and rejected in any other casing.
const (
	ethertypeIPv4 = "IPv4"
	ethertypeIPv6 = "IPv6"
)

// nameCharset is the restriction on names and descriptions, applied here so a
// rejected request becomes a plan-time error.
var nameCharset = regexp.MustCompile(`^[^<>&"']*$`)

const nameCharsetMessage = `must not contain <, >, &, ' or " — the API rejects them`

// cookedRuleSchema describes the display-only rules the details endpoint
// reports, shared by the resource and the singular data source. Everything in it
// is Computed: these are what the platform displays, not the managed rules.
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

// ruleID pairs the group and the rule: Read finds a rule by listing the group's
// rules, and a rule alone cannot say which group to list. Also the import format.
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
