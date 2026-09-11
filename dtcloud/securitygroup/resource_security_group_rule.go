package securitygroup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/config"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceDtcloudSecurityGroupRule manages one rule inside a security group.
//
// The whole resource is ForceNew: there is create and delete and nothing else.
// The id is `<security-group-id>:<rule-id>` — it has to carry the group, since
// Read finds the rule by listing that group's rules. See the package comment.
func ResourceDtcloudSecurityGroupRule() *schema.Resource {
	return &schema.Resource{
		Description: "Manages one rule inside a security group.\n\n" +
			"The API has a create endpoint and a delete endpoint and nothing else, so the whole resource " +
			"forces replacement. That is exactly what the platform does anyway -- delete the old rule, " +
			"create the new one -- and the plan says so.\n\n" +
			"The resource ID is `<security-group-id>:<rule-id>`: a rule is read by listing its group's " +
			"rules, and a bare rule id cannot say which group to list.",

		CreateContext: resourceDtcloudSecurityGroupRuleCreate,
		ReadContext:   resourceDtcloudSecurityGroupRuleRead,
		DeleteContext: resourceDtcloudSecurityGroupRuleDelete,
		Importer: &schema.ResourceImporter{
			StateContext: resourceDtcloudSecurityGroupRuleImport,
		},

		Schema: map[string]*schema.Schema{
			"security_group_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
				Description:  "ID of the security group this rule belongs to.",
			},
			"direction": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringInSlice([]string{directionIngress, directionEgress}, false),
				Description:  "Traffic direction: `ingress` (inbound) or `egress` (outbound).",
			},
			"ethertype": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
				// Exactly these two spellings: the API is case-sensitive.
				ValidateFunc: validation.StringInSlice([]string{ethertypeIPv4, ethertypeIPv6}, false),
				Description:  "Address family: `IPv4` or `IPv6`. Defaults to whatever the platform picks, which is `IPv4`.",
			},
			"protocol": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				// Lowercased on the way out and into state, since the platform stores
				// it lowercase — `protocol = "TCP"` would otherwise never settle.
				StateFunc: func(v interface{}) string { return strings.ToLower(v.(string)) },
				Description: "Protocol the rule matches, e.g. `tcp`, `udp`, `icmp`, or a protocol number. " +
					"Omit it to match every protocol. Not restricted to a fixed list, because the platform accepts " +
					"more than the common names.",
			},
			"port_range_min": {
				Type:         schema.TypeInt,
				Optional:     true,
				ForceNew:     true,
				ValidateFunc: validation.IntBetween(0, 65535),
				Description: "First port in the range. For `icmp` this is the ICMP **type**, not a port. " +
					"Omit for all ports. See the note on 0 below.",
			},
			"port_range_max": {
				Type:         schema.TypeInt,
				Optional:     true,
				ForceNew:     true,
				ValidateFunc: validation.IntBetween(0, 65535),
				Description: "Last port in the range. For `icmp` this is the ICMP **code**, not a port. " +
					"Omit for all ports.",
			},
			"remote_ip_prefix": {
				Type:          schema.TypeString,
				Optional:      true,
				ForceNew:      true,
				ValidateFunc:  validation.IsCIDR,
				ConflictsWith: []string{"remote_group_id"},
				Description:   "CIDR the rule applies to, e.g. `10.0.0.0/24`. Mutually exclusive with `remote_group_id`.",
			},
			"remote_group_id": {
				Type:          schema.TypeString,
				Optional:      true,
				ForceNew:      true,
				ConflictsWith: []string{"remote_ip_prefix"},
				Description: "ID of another security group whose members the rule applies to. An **id**, not a name — " +
					"the platform does not resolve names here. Mutually exclusive with `remote_ip_prefix`.",
			},

			"description": {
				Type:     schema.TypeString,
				Computed: true,
				Description: "Description of the rule. Read-only: the create route does not accept one, so this is " +
					"only ever non-empty for a rule created outside Terraform.",
			},
			"normalized_cidr": {
				Type:     schema.TypeString,
				Computed: true,
				Description: "`remote_ip_prefix` with its host bits cleared, as the platform computes it — " +
					"`10.0.0.5/24` is reported here as `10.0.0.0/24`.",
			},
		},

		// Three rules the API enforces but only after the plan looked fine.
		CustomizeDiff: func(ctx context.Context, d *schema.ResourceDiff, meta interface{}) error {
			protocol := strings.ToLower(d.Get("protocol").(string))
			min := d.Get("port_range_min").(int)
			max := d.Get("port_range_max").(int)
			ethertype := d.Get("ethertype").(string)
			prefix := d.Get("remote_ip_prefix").(string)

			if (min != 0 || max != 0) && protocol == "" {
				return fmt.Errorf("protocol is required when a port range is given: a rule that matches every " +
					"protocol cannot also restrict ports, and the platform refuses it")
			}

			// Only for tcp and udp. For icmp the two fields are type and code,
			// where code < type is perfectly ordinary — type 8 code 0 is a ping.
			if (protocol == "tcp" || protocol == "udp") && min != 0 && max != 0 && min > max {
				return fmt.Errorf("port_range_min (%d) must not be greater than port_range_max (%d)", min, max)
			}

			if ethertype != "" && prefix != "" {
				if family := cidrFamily(prefix); family != "" && family != ethertype {
					return fmt.Errorf("remote_ip_prefix %q is %s but ethertype is %s; the platform rejects a rule "+
						"whose prefix and address family disagree", prefix, family, ethertype)
				}
			}

			return nil
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(5 * time.Minute),
			Delete: schema.DefaultTimeout(5 * time.Minute),
		},
	}
}

// createdRuleID digs the new rule's id out of the raw create response. The rule
// is usually wrapped, but the wrapper is stripped in some deployments, so a bare
// object is accepted too.
func createdRuleID(body string) (string, error) {
	var parsed struct {
		ID   string `json:"id"`
		Rule struct {
			ID string `json:"id"`
		} `json:"security_group_rule"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return "", fmt.Errorf("the API response was not a JSON object: %w (body: %s)", err, body)
	}
	if parsed.Rule.ID != "" {
		return parsed.Rule.ID, nil
	}
	if parsed.ID != "" {
		return parsed.ID, nil
	}
	return "", fmt.Errorf("no rule id in the API response (body: %s)", body)
}

func resourceDtcloudSecurityGroupRuleCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()
	groupID := d.Get("security_group_id").(string)

	params := dtgo.CreateSecurityGroupRuleParams{
		Direction:      d.Get("direction").(string),
		Ethertype:      d.Get("ethertype").(string),
		Protocol:       strings.ToLower(d.Get("protocol").(string)),
		PortRangeMin:   d.Get("port_range_min").(int),
		PortRangeMax:   d.Get("port_range_max").(int),
		RemoteIpPrefix: d.Get("remote_ip_prefix").(string),
		RemoteGroupId:  d.Get("remote_group_id").(string),
	}

	body, err := client.SecurityGroup.CreateSecurityGroupRule(ctx, groupID, params, nil)
	if err != nil {
		return diag.Errorf("Error creating rule in security group %q: %s", groupID, err)
	}

	id, err := createdRuleID(body)
	if err != nil {
		return diag.Errorf("Error reading the created rule's id: %s", err)
	}
	d.SetId(ruleID(groupID, id))

	return resourceDtcloudSecurityGroupRuleRead(ctx, d, meta)
}

func resourceDtcloudSecurityGroupRuleRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	groupID, id, err := splitRuleID(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}

	rules, _, err := client.SecurityGroup.GetSecurityGroupRules(ctx, groupID, nil)
	if err != nil {
		if dterr.IsNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.Errorf("Error listing rules of security group %q: %s", groupID, err)
	}

	for _, r := range rules.SecurityGroupRules {
		if r.ID != id {
			continue
		}
		d.Set("security_group_id", groupID)
		d.Set("direction", r.Direction)
		d.Set("ethertype", r.Ethertype)
		d.Set("protocol", r.Protocol)
		d.Set("port_range_min", r.PortRangeMin)
		d.Set("port_range_max", r.PortRangeMax)
		d.Set("remote_ip_prefix", r.RemoteIPPrefix)
		d.Set("remote_group_id", r.RemoteGroupID)
		d.Set("description", r.Description)
		d.Set("normalized_cidr", r.NormalizedCidr)
		return nil
	}

	// The rule, or the whole group, is gone. The list is filtered by group id
	// without checking the group exists, so a deleted group answers with an empty
	// list rather than a 404; both mean the rule is not there.
	d.SetId("")
	return nil
}

func resourceDtcloudSecurityGroupRuleDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*config.CombinedConfig).DTClient()

	groupID, id, err := splitRuleID(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}

	if _, err := client.SecurityGroup.DeleteSecurityGroupRule(ctx, groupID, id, nil); err != nil {
		if !dterr.IsNotFound(err) {
			return diag.Errorf("Error deleting rule %q from security group %q: %s", id, groupID, err)
		}
	}

	d.SetId("")
	return nil
}

func resourceDtcloudSecurityGroupRuleImport(ctx context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	groupID, id, err := splitRuleID(d.Id())
	if err != nil {
		return nil, err
	}
	d.Set("security_group_id", groupID)
	d.SetId(ruleID(groupID, id))
	return []*schema.ResourceData{d}, nil
}
