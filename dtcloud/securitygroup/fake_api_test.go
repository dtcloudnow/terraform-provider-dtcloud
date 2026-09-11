package securitygroup_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
)

// fakeSecurityGroupAPI stands in for the security group routes. It reproduces
// four behaviours the API has: a 404 with no numeric code, so dterr.IsNotFound
// falls back to the message; a 204 with an empty body on delete; an empty list
// rather than a 404 for the rules of a missing group; and a details endpoint
// that renders its rules for display, which is why rules are read raw.
type fakeSecurityGroupAPI struct {
	mu     sync.Mutex
	groups map[string]*fakeGroup
	rules  map[string]*fakeRule
	seq    int

	// creates counts group POSTs, so a test can prove an in-place update did not
	// quietly rebuild the group.
	creates int
	// groupUpdates records the update bodies, so a test can prove `name` went
	// out even when only the description changed.
	groupUpdates []map[string]any
	// inUse marks groups whose delete must fail the way one still bound to a
	// port does.
	inUse map[string]bool
	// callerIP is what the caller-address route reports.
	callerIP string
}

type fakeGroup struct {
	ID          string
	Name        string
	Description string
	deleted     bool
}

// fakeRule keeps the nullable fields as pointers: null and 0 are different
// things on the wire, and collapsing them would hide whether the provider copes.
type fakeRule struct {
	ID             string
	GroupID        string
	Direction      string
	Ethertype      string
	Protocol       *string
	PortRangeMin   *int
	PortRangeMax   *int
	RemoteIPPrefix *string
	RemoteGroupID  *string
	Description    string
	deleted        bool
}

// apiCreatedAt is the timestamp these objects carry: RFC 3339, with a zone.
const apiCreatedAt = "2026-07-08T10:35:17Z"

// fakeTenantID stands in for the project id echoed on every object.
const fakeTenantID = "82c57cfbb429442989a5695a2a9780f3"

func newFakeSecurityGroupAPI() *fakeSecurityGroupAPI {
	return &fakeSecurityGroupAPI{
		groups:   map[string]*fakeGroup{},
		rules:    map[string]*fakeRule{},
		inUse:    map[string]bool{},
		callerIP: "203.0.113.7",
	}
}

func (f *fakeSecurityGroupAPI) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%04d", prefix, f.seq)
}

// apiError writes the body a failure produces once the API has wrapped it.
func apiError(w http.ResponseWriter, status int, kind, message string) {
	acctest.WriteJSON(w, status, map[string]any{
		"error": map[string]any{
			"NeutronError": map[string]any{
				"type":    kind,
				"message": message,
				"detail":  "",
			},
		},
		"code": "SERVER_ERROR",
	})
}

// Copied verbatim from a live 404 — note there is no trailing full stop.
// dterr.IsNotFound matches on "does not exist", so the wording is load-bearing.
func groupNotFound(w http.ResponseWriter, id string) {
	apiError(w, http.StatusNotFound, "SecurityGroupNotFound",
		fmt.Sprintf("Security group %s does not exist", id))
}

func ruleNotFound(w http.ResponseWriter, id string) {
	apiError(w, http.StatusNotFound, "SecurityGroupRuleNotFound",
		fmt.Sprintf("Security group rule %s does not exist", id))
}

// noContent is what both delete routes actually answer with: 204, no body.
func noContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// validationError is the 406 the API produces.
func validationError(w http.ResponseWriter, message string) {
	acctest.WriteJSON(w, http.StatusNotAcceptable, map[string]any{
		"error": message,
		"code":  "VALIDATION_ERROR",
	})
}

// nameCharsetOK is the API's own character rule.
func nameCharsetOK(s string) bool {
	return !strings.ContainsAny(s, `<>&"'`)
}

func (f *fakeSecurityGroupAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/openstack/securitygroups")
	seg := []string{}
	for _, s := range strings.Split(strings.Trim(path, "/"), "/") {
		if s != "" {
			seg = append(seg, s)
		}
	}

	// Registered before /:securityGroupId and, unlike the other routes here,
	// needs the key pair but not a serverId.
	if r.Method == http.MethodGet && len(seg) == 1 && seg[0] == "ip" {
		if r.Header.Get("x-api-access-key") == "" || r.Header.Get("x-api-secret-key") == "" {
			acctest.WriteJSON(w, http.StatusUnauthorized, map[string]any{"errorMessage": "missing api key headers"})
			return
		}
		f.mu.Lock()
		ip := f.callerIP
		f.mu.Unlock()
		acctest.WriteJSON(w, http.StatusOK, map[string]any{"ip": ip})
		return
	}

	if !acctest.RequireAuth(w, r) {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && len(seg) == 0:
		f.list(w)
	case r.Method == http.MethodPost && len(seg) == 0:
		f.create(w, r)
	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "details":
		f.details(w, seg[0])
	case r.Method == http.MethodPut && len(seg) == 1:
		f.update(w, r, seg[0])
	case r.Method == http.MethodDelete && len(seg) == 1:
		f.deleteGroup(w, seg[0])
	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "rules":
		f.listRules(w, seg[0])
	case r.Method == http.MethodPost && len(seg) == 2 && seg[1] == "rules":
		f.createRule(w, r, seg[0])
	case r.Method == http.MethodGet && len(seg) == 4 && seg[1] == "rules" && seg[3] == "details":
		f.ruleDetails(w, seg[2])
	case r.Method == http.MethodDelete && len(seg) == 3 && seg[1] == "rules":
		f.deleteRule(w, seg[2])
	default:
		acctest.NotFound(w, "no such route")
	}
}

func (f *fakeSecurityGroupAPI) group(id string) *fakeGroup {
	g, ok := f.groups[id]
	if !ok || g.deleted {
		return nil
	}
	return g
}

func (f *fakeSecurityGroupAPI) list(w http.ResponseWriter) {
	// The route takes no query parameter, so filtering is the client's problem,
	// which is why the data source does it itself.
	out := []map[string]any{}
	for _, g := range f.groups {
		if g.deleted {
			continue
		}
		out = append(out, map[string]any{"id": g.ID, "name": g.Name, "description": g.Description})
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeSecurityGroupAPI) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed body"})
		return
	}
	if body.Name == "" {
		validationError(w, "'name' is required")
		return
	}
	if !nameCharsetOK(body.Name) || !nameCharsetOK(body.Description) {
		validationError(w, `Invalid characters in "name". HTML tags and special characters (<, >, &, ', ") are not allowed.`)
		return
	}

	g := &fakeGroup{ID: f.nextID("sg"), Name: body.Name, Description: body.Description}
	f.groups[g.ID] = g
	f.creates++

	// Two egress rules are added to every new group, one per address family.
	// They are not Terraform's, and a test asserts they show up in the snapshot.
	defaults := []*fakeRule{
		{ID: f.nextID("sgr"), GroupID: g.ID, Direction: "egress", Ethertype: "IPv4"},
		{ID: f.nextID("sgr"), GroupID: g.ID, Direction: "egress", Ethertype: "IPv6"},
	}
	created := make([]any, 0, len(defaults))
	for _, rule := range defaults {
		f.rules[rule.ID] = rule
		created = append(created, map[string]any{
			"id": rule.ID, "security_group_id": g.ID,
			"ethertype": rule.Ethertype, "direction": rule.Direction,
			"created_at": apiCreatedAt, "updated_at": apiCreatedAt,
			"revision_number": 0,
		})
	}

	acctest.WriteJSON(w, http.StatusOK, map[string]any{
		"security_group": map[string]any{
			"id": g.ID, "name": g.Name, "stateful": true, "tenant_id": fakeTenantID,
			"description":          g.Description,
			"security_group_rules": created,
			"created_at":           apiCreatedAt, "updated_at": apiCreatedAt,
			"revision_number": 0,
		},
	})
}

func (f *fakeSecurityGroupAPI) update(w http.ResponseWriter, r *http.Request, id string) {
	g := f.group(id)
	if g == nil {
		groupNotFound(w, id)
		return
	}

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed body"})
		return
	}

	record := map[string]any{}
	blob, _ := json.Marshal(raw)
	_ = json.Unmarshal(blob, &record)
	f.groupUpdates = append(f.groupUpdates, record)

	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(blob, &body)

	// The update route requires a name: a description-only patch is refused,
	// which is why the provider always sends both.
	if body.Name == "" {
		validationError(w, "'name' is required")
		return
	}
	if !nameCharsetOK(body.Name) || !nameCharsetOK(body.Description) {
		validationError(w, `Invalid characters in "name".`)
		return
	}

	g.Name = body.Name
	// Only a description that was actually sent is applied. dt-go sends it as a
	// pointer, so an empty string clears the value while a nil one leaves the key
	// out and the platform keeps what it had.
	if _, sent := raw["description"]; sent {
		g.Description = body.Description
	}

	acctest.WriteJSON(w, http.StatusOK, map[string]any{
		"security_group": map[string]any{
			"id": g.ID, "name": g.Name, "stateful": true, "description": g.Description,
			"security_group_rules": []any{},
			"created_at":           apiCreatedAt, "updated_at": apiCreatedAt,
			"revision_number": 1,
		},
	})
}

func (f *fakeSecurityGroupAPI) deleteGroup(w http.ResponseWriter, id string) {
	g := f.group(id)
	if g == nil {
		groupNotFound(w, id)
		return
	}
	if f.inUse[id] {
		apiError(w, http.StatusConflict, "SecurityGroupInUse",
			fmt.Sprintf("Security Group %s in use.", id))
		return
	}
	g.deleted = true
	for _, rule := range f.rules {
		if rule.GroupID == id {
			rule.deleted = true
		}
	}
	noContent(w)
}

func (f *fakeSecurityGroupAPI) rulesOf(groupID string) []*fakeRule {
	out := []*fakeRule{}
	for _, rule := range f.rules {
		if rule.GroupID == groupID && !rule.deleted {
			out = append(out, rule)
		}
	}
	// Deterministic order: sorting by id keeps the list stable across runs.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].ID > out[j].ID; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// ruleBody is the rule as it appears on the wire, a struct in the API's field
// order — `description` after `created_at`.
type ruleBody struct {
	ID                   string  `json:"id"`
	TenantID             string  `json:"tenant_id"`
	SecurityGroupID      string  `json:"security_group_id"`
	Ethertype            string  `json:"ethertype"`
	Direction            string  `json:"direction"`
	Protocol             *string `json:"protocol"`
	PortRangeMin         *int    `json:"port_range_min"`
	PortRangeMax         *int    `json:"port_range_max"`
	RemoteIPPrefix       *string `json:"remote_ip_prefix"`
	RemoteAddressGroupID *string `json:"remote_address_group_id"`
	NormalizedCidr       *string `json:"normalized_cidr"`
	RemoteGroupID        *string `json:"remote_group_id"`
	Description          string  `json:"description"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
	RevisionNumber       int     `json:"revision_number"`
	ProjectID            string  `json:"project_id"`
}

func (rule *fakeRule) body() ruleBody {
	return ruleBody{
		ID:              rule.ID,
		TenantID:        fakeTenantID,
		SecurityGroupID: rule.GroupID,
		Ethertype:       rule.Ethertype,
		Direction:       rule.Direction,
		Protocol:        rule.Protocol,
		PortRangeMin:    rule.PortRangeMin,
		PortRangeMax:    rule.PortRangeMax,
		RemoteIPPrefix:  rule.RemoteIPPrefix,
		NormalizedCidr:  rule.RemoteIPPrefix,
		RemoteGroupID:   rule.RemoteGroupID,
		CreatedAt:       apiCreatedAt,
		UpdatedAt:       apiCreatedAt,
		Description:     rule.Description,
		ProjectID:       fakeTenantID,
	}
}

func (f *fakeSecurityGroupAPI) listRules(w http.ResponseWriter, groupID string) {
	// No 404 for an unknown group: what matches is returned, which for a group
	// that was deleted is nothing.
	out := []ruleBody{}
	for _, rule := range f.rulesOf(groupID) {
		out = append(out, rule.body())
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"security_group_rules": out})
}

func (f *fakeSecurityGroupAPI) ruleDetails(w http.ResponseWriter, ruleID string) {
	rule, ok := f.rules[ruleID]
	if !ok || rule.deleted {
		ruleNotFound(w, ruleID)
		return
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"security_group_rule": rule.body()})
}

func (f *fakeSecurityGroupAPI) createRule(w http.ResponseWriter, r *http.Request, groupID string) {
	if f.group(groupID) == nil {
		groupNotFound(w, groupID)
		return
	}

	var body struct {
		Direction      string  `json:"direction"`
		Ethertype      string  `json:"ethertype"`
		Protocol       *string `json:"protocol"`
		PortRangeMin   *int    `json:"port_range_min"`
		PortRangeMax   *int    `json:"port_range_max"`
		RemoteIPPrefix *string `json:"remote_ip_prefix"`
		RemoteGroupID  *string `json:"remote_group_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed body"})
		return
	}

	// direction required and constrained, ethertype constrained when present.
	if body.Direction != "ingress" && body.Direction != "egress" {
		validationError(w, "'direction' must be one of [ingress, egress]")
		return
	}
	if body.Ethertype != "" && body.Ethertype != "IPv4" && body.Ethertype != "IPv6" {
		validationError(w, "'ethertype' must be one of [IPv4, IPv6]")
		return
	}

	ethertype := body.Ethertype
	if ethertype == "" {
		ethertype = "IPv4" // Neutron's default
	}

	rule := &fakeRule{
		ID: f.nextID("sgr"), GroupID: groupID,
		Direction: body.Direction, Ethertype: ethertype,
		Protocol: body.Protocol, PortRangeMin: body.PortRangeMin, PortRangeMax: body.PortRangeMax,
		RemoteIPPrefix: body.RemoteIPPrefix, RemoteGroupID: body.RemoteGroupID,
	}

	// An exact duplicate is refused.
	for _, existing := range f.rulesOf(groupID) {
		if sameRule(existing, rule) {
			apiError(w, http.StatusConflict, "SecurityGroupRuleExists",
				fmt.Sprintf("Security group rule already exists. Rule id is %s.", existing.ID))
			return
		}
	}

	f.rules[rule.ID] = rule
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"security_group_rule": rule.body()})
}

func sameRule(a, b *fakeRule) bool {
	return a.Direction == b.Direction &&
		a.Ethertype == b.Ethertype &&
		derefString(a.Protocol) == derefString(b.Protocol) &&
		derefInt(a.PortRangeMin) == derefInt(b.PortRangeMin) &&
		derefInt(a.PortRangeMax) == derefInt(b.PortRangeMax) &&
		derefString(a.RemoteIPPrefix) == derefString(b.RemoteIPPrefix) &&
		derefString(a.RemoteGroupID) == derefString(b.RemoteGroupID)
}

func (f *fakeSecurityGroupAPI) deleteRule(w http.ResponseWriter, ruleID string) {
	rule, ok := f.rules[ruleID]
	if !ok || rule.deleted {
		ruleNotFound(w, ruleID)
		return
	}
	rule.deleted = true
	noContent(w)
}

// The details endpoint, and its cooking.

func (f *fakeSecurityGroupAPI) details(w http.ResponseWriter, id string) {
	g := f.group(id)
	if g == nil {
		groupNotFound(w, id)
		return
	}

	inbound := []map[string]any{}
	outbound := []map[string]any{}
	for _, rule := range f.rulesOf(id) {
		cooked := f.cookRule(rule)
		if rule.Direction == "ingress" {
			inbound = append(inbound, cooked)
		} else {
			outbound = append(outbound, cooked)
		}
	}

	acctest.WriteJSON(w, http.StatusOK, map[string]any{
		"id": g.ID, "name": g.Name, "description": g.Description,
		"inboundRules": inbound, "outboundRules": outbound,
	})
}

// cookRule renders a rule the way the details endpoint does. The display names
// are the platform's fallbacks and are configurable there, which is the argument
// for treating any of this as presentation and nothing else.
func (f *fakeSecurityGroupAPI) cookRule(rule *fakeRule) map[string]any {
	var protocolName, portRange string

	min, max := rule.PortRangeMin, rule.PortRangeMax
	proto := rule.Protocol

	switch {
	case derefInt(max) == derefInt(min) && (max == nil) == (min == nil):
		if max == nil {
			if derefString(proto) == "icmp" {
				portRange = "-"
			} else {
				portRange = "1-65535"
			}
		} else {
			portRange = fmt.Sprintf("%d", *max)
		}
		if derefString(proto) == "tcp" {
			switch derefInt(max) {
			case 445:
				protocolName = "SMB"
			case 22:
				protocolName = "SSH"
			case 80:
				protocolName = "HTTP"
			case 443:
				protocolName = "HTTPS"
			default:
				protocolName = "TCP"
			}
		} else if proto == nil {
			protocolName = "ANY"
		} else {
			protocolName = strings.ToUpper(*proto)
		}
	case derefInt(min) == 1 && derefInt(max) == 65535 && proto != nil:
		portRange = "1-65535"
		protocolName = "ALL " + strings.ToUpper(*proto)
	case derefString(proto) == "icmp" && derefInt(min) == 1 && derefInt(max) == 255:
		portRange = "1-255"
		protocolName = "ALL ICMP"
	default:
		if max == nil {
			portRange = "1-65535"
		} else {
			portRange = fmt.Sprintf("%d-%d", derefInt(min), derefInt(max))
		}
		if proto == nil {
			protocolName = "ANY"
		} else {
			protocolName = strings.ToUpper(*proto)
		}
	}

	// An unscoped rule reads as the whole address family, and a rule against
	// another group reads as that group's name.
	source := ""
	switch {
	case rule.RemoteIPPrefix == nil && rule.RemoteGroupID == nil && rule.Ethertype != "IPv6":
		source = "0.0.0.0/0"
	case rule.RemoteIPPrefix == nil && rule.RemoteGroupID == nil:
		source = "::/0"
	case rule.RemoteIPPrefix == nil && rule.RemoteGroupID != nil:
		if other := f.group(*rule.RemoteGroupID); other != nil {
			source = other.Name
		}
	default:
		source = *rule.RemoteIPPrefix
	}

	return map[string]any{
		"id": rule.ID, "protocol": protocolName, "portRange": portRange, "source": source,
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefInt(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}
