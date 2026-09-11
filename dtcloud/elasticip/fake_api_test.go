package elasticip_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
)

// fakeElasticIPAPI stands in for the floating IP routes. It reproduces two
// things the API does: an update with no `port_id` disassociates rather than
// leaving the association alone, which is why the association cannot live in a
// second resource; and `port_details` is an object, which dt-go types as
// *string, so the provider reads it out of the raw body.
type fakeElasticIPAPI struct {
	mu  sync.Mutex
	ips map[string]*fakeIP
	seq int

	// creates counts allocations, so a test can prove that moving an address
	// between ports did not quietly release and re-allocate it.
	creates int
	// updates records the bodies sent, so a test can prove that disassociating
	// really did send an empty object.
	updates []map[string]any
	// ports maps a port id to the device behind it, which is what details reports
	// as port_details and the list cooks into a name.
	ports map[string]fakePort
	// networkNames maps an external network id to the name the list reports.
	networkNames map[string]string
	// failCreate makes the next allocation fail the way an exhausted quota does.
	failCreate bool
}

type fakePort struct {
	DeviceID    string
	DeviceOwner string
	DeviceName  string
	Kind        string // "VM" or "LB"
	FixedIP     string
	// multiIP marks a port carrying more than one IPv4 address. Such a port
	// refuses a floating IP unless the request names which address to map to.
	multiIP bool
}

type fakeIP struct {
	ID        string
	Address   string
	NetworkID string
	SubnetID  string
	PortID    string
	FixedIP   string
	deleted   bool
}

// apiCreatedAt is the timestamp these objects carry: RFC 3339, with a zone.
const apiCreatedAt = "2026-08-12T12:34:25Z"

const fakeTenantID = "82c57cfbb429442989a5695a2a9780f3"

func newFakeElasticIPAPI() *fakeElasticIPAPI {
	return &fakeElasticIPAPI{
		ips:   map[string]*fakeIP{},
		ports: map[string]fakePort{},
		networkNames: map[string]string{
			"ext-net-1": "Firewall_198_51_100_0",
		},
	}
}

// withPort registers a port a test can associate an address with, standing in
// for a VM's interface.
func (f *fakeElasticIPAPI) withPort(portID, deviceID, name, kind, fixedIP string) *fakeElasticIPAPI {
	owner := "compute:nova"
	if kind == "LB" {
		owner = "Octavia"
	}
	f.ports[portID] = fakePort{DeviceID: deviceID, DeviceOwner: owner, DeviceName: name, Kind: kind, FixedIP: fixedIP}
	return f
}

// withMultiIPPort registers a port carrying several IPv4 addresses, which the
// platform refuses to associate without being told which one to use.
func (f *fakeElasticIPAPI) withMultiIPPort(portID, deviceID, name, primary string) *fakeElasticIPAPI {
	f.ports[portID] = fakePort{
		DeviceID: deviceID, DeviceOwner: "compute:nova", DeviceName: name,
		Kind: "VM", FixedIP: primary, multiIP: true,
	}
	return f
}

// requireFixedIP reproduces the refusal a multi-address port gives:
//
//	Bad floatingip request: Port <id> has multiple fixed IPv4 addresses.
//	Must provide a specific IPv4 address when assigning a floating IP.
func requireFixedIP(w http.ResponseWriter, portID string) {
	apiError(w, http.StatusBadRequest, "BadRequest",
		fmt.Sprintf("Bad floatingip request: Port %s has multiple fixed IPv4 addresses.  "+
			"Must provide a specific IPv4 address when assigning a floating IP.", portID))
}

func (f *fakeElasticIPAPI) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%04d", prefix, f.seq)
}

// apiError is the body a failure produces once the API has wrapped it: no
// numeric code anywhere, so dterr.IsNotFound has to match the message text.
func apiError(w http.ResponseWriter, status int, kind, message string) {
	acctest.WriteJSON(w, status, map[string]any{
		"error": map[string]any{
			"NeutronError": map[string]any{"type": kind, "message": message, "detail": ""},
		},
		"code": "SERVER_ERROR",
	})
}

func ipNotFound(w http.ResponseWriter, id string) {
	apiError(w, http.StatusNotFound, "FloatingIPNotFound",
		fmt.Sprintf("Floating IP %s could not be found", id))
}

func validationError(w http.ResponseWriter, message string) {
	acctest.WriteJSON(w, http.StatusNotAcceptable, map[string]any{
		"error": message,
		"code":  "VALIDATION_ERROR",
	})
}

func (f *fakeElasticIPAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !acctest.RequireAuth(w, r) {
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/openstack/floatingips")
	seg := []string{}
	for _, s := range strings.Split(strings.Trim(path, "/"), "/") {
		if s != "" {
			seg = append(seg, s)
		}
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
		f.delete(w, seg[0])
	default:
		acctest.NotFound(w, "no such route")
	}
}

func (f *fakeElasticIPAPI) get(id string) *fakeIP {
	ip, ok := f.ips[id]
	if !ok || ip.deleted {
		return nil
	}
	return ip
}

// nullable renders a value the way the API does: the JSON null, not "".
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// body is the raw floating IP, in the field order a live response uses. Every
// field moves at once — the reply already carries the new association — so there
// is no settling window to reproduce.
func (f *fakeElasticIPAPI) body(ip *fakeIP) map[string]any {
	out := map[string]any{
		"id":                  ip.ID,
		"tenant_id":           fakeTenantID,
		"floating_ip_address": ip.Address,
		"floating_network_id": ip.NetworkID,
		"router_id":           nullable(f.routerFor(ip)),
		"port_id":             nullable(ip.PortID),
		"fixed_ip_address":    nullable(ip.FixedIP),
		"status":              f.statusOf(ip),
		"description":         "",
		"qos_policy_id":       nil,
		"port_details":        nil,
		"tags":                []any{},
		"created_at":          apiCreatedAt,
		"updated_at":          apiCreatedAt,
		"revision_number":     0,
		"project_id":          fakeTenantID,
	}
	// An object, not a string. dt-go's type for this field is wrong, so the
	// provider parses it out of the raw body.
	if port, ok := f.ports[ip.PortID]; ok && ip.PortID != "" {
		out["port_details"] = map[string]any{
			"name":           "",
			"network_id":     "int-net-1",
			"mac_address":    "fa:16:3e:00:00:01",
			"admin_state_up": true,
			"status":         "ACTIVE",
			"device_id":      port.DeviceID,
			"device_owner":   port.DeviceOwner,
		}
	}
	return out
}

func (f *fakeElasticIPAPI) statusOf(ip *fakeIP) string {
	if ip.PortID == "" {
		return "DOWN"
	}
	return "ACTIVE"
}

func (f *fakeElasticIPAPI) routerFor(ip *fakeIP) string {
	if ip.PortID == "" {
		return ""
	}
	return "router-0001"
}

func (f *fakeElasticIPAPI) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FloatingNetworkID string `json:"floating_network_id"`
		PortID            string `json:"port_id"`
		SubnetID          string `json:"subnet_id"`
		FixedIPAddress    string `json:"fixed_ip_address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed body"})
		return
	}
	// floating_network_id is the only required field.
	if body.FloatingNetworkID == "" {
		validationError(w, "'floating_network_id' is required")
		return
	}
	if f.failCreate {
		apiError(w, http.StatusConflict, "OverQuota",
			"Quota exceeded for resources: ['floatingip']")
		return
	}

	f.seq++
	ip := &fakeIP{
		ID:        fmt.Sprintf("fip-%04d", f.seq),
		Address:   fmt.Sprintf("198.51.100.%d", 10+f.seq),
		NetworkID: body.FloatingNetworkID,
		SubnetID:  body.SubnetID,
		PortID:    body.PortID,
	}
	if ip.PortID != "" {
		port, ok := f.ports[ip.PortID]
		if !ok {
			apiError(w, http.StatusNotFound, "PortNotFound",
				fmt.Sprintf("Port %s could not be found", ip.PortID))
			return
		}
		if port.multiIP && body.FixedIPAddress == "" {
			requireFixedIP(w, ip.PortID)
			return
		}
		if body.FixedIPAddress != "" && body.FixedIPAddress != port.FixedIP {
			apiError(w, http.StatusBadRequest, "ExternalIpAddressExhausted",
				fmt.Sprintf("Port %s does not have fixed ip %s", ip.PortID, body.FixedIPAddress))
			return
		}
		ip.FixedIP = body.FixedIPAddress
		if ip.FixedIP == "" {
			ip.FixedIP = port.FixedIP
		}
	}
	f.ips[ip.ID] = ip
	f.creates++

	acctest.WriteJSON(w, http.StatusOK, f.body(ip))
}

func (f *fakeElasticIPAPI) details(w http.ResponseWriter, id string) {
	ip := f.get(id)
	if ip == nil {
		ipNotFound(w, id)
		return
	}
	acctest.WriteJSON(w, http.StatusOK, f.body(ip))
}

func (f *fakeElasticIPAPI) update(w http.ResponseWriter, r *http.Request, id string) {
	ip := f.get(id)
	if ip == nil {
		ipNotFound(w, id)
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
	f.updates = append(f.updates, record)

	var body struct {
		PortID         string `json:"port_id"`
		FixedIPAddress string `json:"fixed_ip_address"`
	}
	_ = json.Unmarshal(blob, &body)

	// The behaviour the whole design rests on: no port_id in the body means
	// disassociate, not "leave it alone".
	if body.PortID == "" {
		ip.PortID = ""
		ip.FixedIP = ""
		acctest.WriteJSON(w, http.StatusOK, f.body(ip))
		return
	}

	port, ok := f.ports[body.PortID]
	if !ok {
		apiError(w, http.StatusNotFound, "PortNotFound",
			fmt.Sprintf("Port %s could not be found", body.PortID))
		return
	}
	if port.multiIP && body.FixedIPAddress == "" {
		requireFixedIP(w, body.PortID)
		return
	}
	// An address only maps to one of the port's own addresses. Sending the
	// previous port's fixed IP while moving the floating IP is the bug this
	// rejection exists to catch.
	if body.FixedIPAddress != "" && body.FixedIPAddress != port.FixedIP {
		apiError(w, http.StatusBadRequest, "ExternalIpAddressExhausted",
			fmt.Sprintf("Port %s does not have fixed ip %s", body.PortID, body.FixedIPAddress))
		return
	}
	ip.PortID = body.PortID
	ip.FixedIP = body.FixedIPAddress
	if ip.FixedIP == "" {
		ip.FixedIP = port.FixedIP
	}
	acctest.WriteJSON(w, http.StatusOK, f.body(ip))
}

func (f *fakeElasticIPAPI) delete(w http.ResponseWriter, id string) {
	ip := f.get(id)
	if ip == nil {
		ipNotFound(w, id)
		return
	}
	ip.deleted = true
	// A real 204, with no body.
	w.WriteHeader(http.StatusNoContent)
}

// list is the cooked view, and the only place the network's name and the name
// of the machine behind the address appear.
func (f *fakeElasticIPAPI) list(w http.ResponseWriter) {
	out := []map[string]any{}
	for _, ip := range f.ips {
		if ip.deleted {
			continue
		}
		row := map[string]any{
			"id":          ip.ID,
			"ipAddress":   ip.Address,
			"status":      f.statusOf(ip),
			"network":     f.networkNames[ip.NetworkID],
			"networkId":   ip.NetworkID,
			"vmORlb":      "",
			"vmIpAddress": nil,
			"assignedTo":  "",
			"assignedId":  "",
		}
		if port, ok := f.ports[ip.PortID]; ok && ip.PortID != "" {
			row["vmORlb"] = port.Kind
			row["assignedTo"] = port.DeviceName
			row["assignedId"] = port.DeviceID
			row["vmIpAddress"] = ip.FixedIP
		}
		out = append(out, row)
	}
	// Deterministic order: ids are sequential, so sorting by id keeps the list
	// stable without depending on map iteration.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1]["id"].(string) > out[j]["id"].(string); j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}
