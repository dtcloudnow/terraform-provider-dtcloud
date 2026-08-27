package network_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
)

type fakeSubnet struct {
	ID              string
	CIDR            string
	Gateway         string
	DHCP            bool
	DNS             []string
	AllocationPools []map[string]string
}

type fakeNetwork struct {
	ID      string
	Name    string
	Type    string
	IPAM    bool
	deleted bool
	subnet  *fakeSubnet
}

// fakeNetworkAPI stands in for cloud-web-api's /openstack/networks routes.
//
// The rules it enforces are the ones the real API enforces, not the ones this
// provider happens to find convenient — that distinction is the whole point of
// the fake. In particular it reproduces the create response's two shapes and
// the subnet update's rejection of null arrays.
type fakeNetworkAPI struct {
	mu       sync.Mutex
	networks map[string]*fakeNetwork
	seq      int
	creates  int
	// subnetUpdates records the bodies the provider sent, so a test can assert
	// the complete set went out rather than a partial patch.
	subnetUpdates []map[string]any
}

func newFakeNetworkAPI() *fakeNetworkAPI {
	return &fakeNetworkAPI{networks: map[string]*fakeNetwork{}}
}

func (f *fakeNetworkAPI) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%04d", prefix, f.seq)
}

func (f *fakeNetworkAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !acctest.RequireAuth(w, r) {
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/openstack/networks")
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
		f.list(w, r)
	case r.Method == http.MethodPost && len(seg) == 0:
		f.create(w, r)
	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "details":
		f.details(w, seg[0])
	case r.Method == http.MethodPut && len(seg) == 1:
		f.rename(w, r, seg[0])
	case r.Method == http.MethodPut && len(seg) == 3 && seg[1] == "subnets":
		f.updateSubnet(w, r, seg[0], seg[2])
	case r.Method == http.MethodDelete && len(seg) == 1:
		f.delete(w, seg[0])
	default:
		acctest.NotFound(w, "no such route")
	}
}

func (f *fakeNetworkAPI) get(w http.ResponseWriter, id string) *fakeNetwork {
	n, ok := f.networks[id]
	if !ok || n.deleted {
		acctest.NotFound(w, "network not found")
		return nil
	}
	return n
}

func (f *fakeNetworkAPI) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IPAM            *bool               `json:"IP_address_management"`
		Name            string              `json:"name"`
		CIDR            string              `json:"cidr"`
		GatewayIP       string              `json:"gateway_ip"`
		EnableDHCP      bool                `json:"enable_dhcp"`
		DNSNameservers  []string            `json:"dns_nameservers"`
		AllocationPools []map[string]string `json:"allocation_pools"`
		IPVersion       int                 `json:"ip_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "malformed body"})
		return
	}
	// Joi: IP_address_management and name are required.
	if body.IPAM == nil || body.Name == "" {
		acctest.WriteJSON(w, http.StatusNotAcceptable, map[string]any{
			"errorMessage": "'IP_address_management' and 'name' are required"})
		return
	}

	n := &fakeNetwork{ID: f.nextID("net"), Name: body.Name, Type: "Virtual", IPAM: *body.IPAM}
	f.networks[n.ID] = n
	f.creates++

	if !*body.IPAM {
		// No subnet is created, and the response is the bare network object —
		// no wrapper, no subnet. This asymmetry is real and the provider has to
		// dig the id out of it.
		acctest.WriteJSON(w, http.StatusOK, map[string]any{
			"id": n.ID, "name": n.Name, "port_security_enabled": false,
		})
		return
	}

	gateway := body.GatewayIP
	if gateway == "" {
		gateway = defaultGateway(body.CIDR)
	}
	pools := body.AllocationPools
	if len(pools) == 0 {
		pools = []map[string]string{{"start": defaultPoolStart(body.CIDR), "end": defaultPoolEnd(body.CIDR)}}
	}
	dns := body.DNSNameservers
	if dns == nil {
		dns = []string{}
	}
	n.subnet = &fakeSubnet{
		ID: f.nextID("subnet"), CIDR: body.CIDR, Gateway: gateway,
		DHCP: body.EnableDHCP, DNS: dns, AllocationPools: pools,
	}

	// With IPAM on, the API answers with the createSubnet response — so the
	// network's id only appears as network_id, nested one level down.
	acctest.WriteJSON(w, http.StatusOK, map[string]any{
		"subnet": map[string]any{
			"id": n.subnet.ID, "network_id": n.ID, "cidr": n.subnet.CIDR,
			"gateway_ip": n.subnet.Gateway, "enable_dhcp": n.subnet.DHCP,
			"ip_version": 4, "dns_nameservers": n.subnet.DNS,
			"allocation_pools": n.subnet.AllocationPools,
		},
	})
}

func (f *fakeNetworkAPI) details(w http.ResponseWriter, id string) {
	n := f.get(w, id)
	if n == nil {
		return
	}

	conf := map[string]any{"name": n.Name, "type": n.Type, "id": n.ID}
	out := map[string]any{"networkConfiguration": conf}

	// Without IPAM the real response carries neither `ipam` nor `subnets` — it
	// omits them entirely rather than sending empty values. Checked live.
	if n.IPAM && n.subnet != nil {
		conf["ipam"] = "Enabled"
		out["subnets"] = map[string]any{
			"id": n.subnet.ID, "subnetIpVersion": 4, "cidr": n.subnet.CIDR,
			"gateway": n.subnet.Gateway, "dhcp": n.subnet.DHCP,
			"allocation_pools": n.subnet.AllocationPools,
			"dnsServer":        n.subnet.DNS,
		}
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeNetworkAPI) list(w http.ResponseWriter, r *http.Request) {
	nameFilter := r.URL.Query().Get("name")
	typeFilter := r.URL.Query().Get("networkType")

	out := []map[string]any{}
	for _, n := range f.networks {
		if n.deleted {
			continue
		}
		if nameFilter != "" && n.Name != nameFilter {
			continue
		}
		if typeFilter != "" && n.Type != typeFilter {
			continue
		}
		row := map[string]any{
			"name": n.Name, "type": n.Type, "id": n.ID,
			"ipam": "Disabled", "dhcp": "Disabled", "portSecurityEnabled": n.IPAM,
			"subnetId": "", "cidr": "", "gateway": "",
		}
		if n.subnet != nil {
			row["ipam"] = "Enabled"
			row["subnetId"] = n.subnet.ID
			row["cidr"] = n.subnet.CIDR
			row["gateway"] = n.subnet.Gateway
			if n.subnet.DHCP {
				row["dhcp"] = "Enabled"
			}
		}
		out = append(out, row)
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeNetworkAPI) rename(w http.ResponseWriter, r *http.Request, id string) {
	n := f.get(w, id)
	if n == nil {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Name == "" {
		acctest.WriteJSON(w, http.StatusNotAcceptable, map[string]any{"errorMessage": "'name' is required"})
		return
	}
	n.Name = body.Name
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"id": n.ID, "name": n.Name})
}

func (f *fakeNetworkAPI) updateSubnet(w http.ResponseWriter, r *http.Request, id, subnetID string) {
	n := f.get(w, id)
	if n == nil {
		return
	}
	if n.subnet == nil || n.subnet.ID != subnetID {
		acctest.NotFound(w, "subnet not found")
		return
	}

	// Decoded as raw first so that a missing field and an explicit null can be
	// told apart — Joi.array() rejects both, and so does this.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "malformed body"})
		return
	}
	for _, field := range []string{"enable_dhcp", "dns_nameservers", "allocation_pools"} {
		v, present := raw[field]
		if !present || string(v) == "null" {
			acctest.WriteJSON(w, http.StatusNotAcceptable, map[string]any{
				"errorMessage": fmt.Sprintf("'%s' is required and must not be null", field)})
			return
		}
	}

	var body struct {
		EnableDHCP      bool                `json:"enable_dhcp"`
		GatewayIP       string              `json:"gateway_ip"`
		DNSNameservers  []string            `json:"dns_nameservers"`
		AllocationPools []map[string]string `json:"allocation_pools"`
	}
	blob, _ := json.Marshal(raw)
	_ = json.Unmarshal(blob, &body)

	record := map[string]any{}
	_ = json.Unmarshal(blob, &record)
	f.subnetUpdates = append(f.subnetUpdates, record)

	n.subnet.DHCP = body.EnableDHCP
	n.subnet.DNS = body.DNSNameservers
	n.subnet.AllocationPools = body.AllocationPools
	if body.GatewayIP != "" {
		n.subnet.Gateway = body.GatewayIP
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"id": n.subnet.ID})
}

func (f *fakeNetworkAPI) delete(w http.ResponseWriter, id string) {
	n := f.get(w, id)
	if n == nil {
		return
	}
	n.deleted = true
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}

// The platform derives these when the caller leaves them out; the exact values
// do not matter to the tests, only that something is filled in and reported
// back, because that is what Computed has to cope with.
func defaultGateway(cidr string) string   { return prefix(cidr) + ".1" }
func defaultPoolStart(cidr string) string { return prefix(cidr) + ".2" }
func defaultPoolEnd(cidr string) string   { return prefix(cidr) + ".254" }

func prefix(cidr string) string {
	parts := strings.Split(strings.SplitN(cidr, "/", 2)[0], ".")
	if len(parts) < 3 {
		return "10.0.0"
	}
	return strings.Join(parts[:3], ".")
}
