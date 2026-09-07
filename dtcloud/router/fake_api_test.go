package router_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
)

// noHTML is the character rule the API applies to a router name.
var noHTML = regexp.MustCompile(`^[^<>&"']*$`)

// fakeRouterTime is the timestamp these endpoints report.
//
// It carries a timezone, which most of the API does not — hence a constant of
// its own rather than the shared fixture, whose whole purpose is to reproduce
// the commoner shape that has none. dtgo.Time reads both; a fixture is only
// worth anything if it is the shape the service actually sends.
const fakeRouterTime = "2026-07-08T10:35:17Z"

// settleReads is how many reads a change takes to become visible, so the
// waiters are exercised rather than satisfied on their first poll.
//
// It has to exceed three: a wait that watches the wrong thing ends after two
// reads, and the read that follows it costs a third. Below that threshold a
// waiter that returned far too early is indistinguishable from one that waited.
//
// Larger than anything the platform is expected to do. The fake models the case
// the waiter has to survive, not the one that happens to be fast today.
const settleReads = 4

type fakeRoute struct {
	Destination string
	NextHop     string
}

type fakeInterface struct {
	PortID    string
	NetworkID string
	SubnetID  string
	IPAddress string
}

type fakeRouter struct {
	ID           string
	Name         string
	Status       string
	AdminStateUp bool
	Description  string
	ProjectID    string
	UpdatedAt    string

	// The gateway. An empty network id means there is none, which the details
	// endpoint reports as a JSON null.
	ExtNetworkID  string
	ExtEnableSnat bool
	ExtSubnetID   string
	ExtIPAddress  string

	Routes     []fakeRoute
	Interfaces []*fakeInterface

	// Pending changes, each with its own countdown in reads. A change with a
	// delay of n is invisible for n reads.
	pendingName    string
	nameDelay      int
	pendingNetwork string
	pendingSnat    bool
	gatewayDelay   int
	pendingStatus  string
	statusDelay    int
	// The route and interface lists are replaced wholesale, because that is
	// how the endpoints behind them work.
	pendingRoutes []fakeRoute
	routesDelay   int
	pendingIfaces []*fakeInterface
	ifacesDelay   int

	deleted     bool
	deleteDelay int
}

// tick advances every pending change by one read. Callers report the router
// before calling this, so a change is invisible for exactly its delay.
func (r *fakeRouter) tick() {
	if r.deleteDelay > 0 {
		r.deleteDelay--
		if r.deleteDelay == 0 {
			r.deleted = true
		}
	}
	if r.statusDelay > 0 {
		r.statusDelay--
		if r.statusDelay == 0 {
			r.Status = r.pendingStatus
		}
	}
	if r.nameDelay > 0 {
		r.nameDelay--
		if r.nameDelay == 0 {
			r.Name = r.pendingName
		}
	}
	if r.gatewayDelay > 0 {
		r.gatewayDelay--
		if r.gatewayDelay == 0 {
			r.ExtNetworkID = r.pendingNetwork
			r.ExtEnableSnat = r.pendingSnat
		}
	}
	if r.routesDelay > 0 {
		r.routesDelay--
		if r.routesDelay == 0 {
			r.Routes = r.pendingRoutes
		}
	}
	if r.ifacesDelay > 0 {
		r.ifacesDelay--
		if r.ifacesDelay == 0 {
			r.Interfaces = r.pendingIfaces
		}
	}
}

// fakeNetwork is the little a router needs to know about a network: the attach
// endpoint resolves the network's first subnet, and the router listing reports
// the external network by name.
type fakeNetwork struct {
	Name     string
	SubnetID string
	Cidr     string
}

// notFound writes the shape these routes send for a missing object.
//
// Deliberately different from every other fake in this provider. The router
// endpoints hand the network layer's own error straight back, and that error
// carries **no numeric status code anywhere in the body** — so the structured
// branch of dterr.IsNotFound finds nothing and the classification rests
// entirely on the message text containing "could not be found". That is a
// thinner thread than the other services hang by, which is why it also has a
// test of its own: TestRouterNotFoundIsClassified.
func notFound(w http.ResponseWriter, kind, message string) {
	acctest.WriteJSON(w, http.StatusNotFound, map[string]any{
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

// badRequest is the other shape the network layer sends: the same envelope as
// notFound, a different status, and again no numeric code inside it.
func badRequest(w http.ResponseWriter, message string) {
	acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"error": map[string]any{
			"NeutronError": map[string]any{
				"type":    "HTTPBadRequest",
				"message": message,
				"detail":  "",
			},
		},
		"code": "SERVER_ERROR",
	})
}

// validationError reproduces the API's rejection body, including the leading
// space and trailing comma the real one carries: the server builds that string
// by reducing its validator's details.
func validationError(w http.ResponseWriter, message string) {
	acctest.WriteJSON(w, http.StatusNotAcceptable, map[string]any{
		"error": " " + message + ",",
		"code":  "VALIDATE_ERROR",
	})
}

// Response fixtures are structs rather than maps so that field order is
// preserved. Go sorts map keys alphabetically, which would move created_at to
// the front and hide the decoding rule it is here to exercise: a timestamp that
// fails to parse takes every field declared after it with it, and
// revision_number and project_id are declared after it.
type fakeFixedIPJSON struct {
	SubnetID  string `json:"subnet_id"`
	IPAddress string `json:"ip_address"`
}

type fakeGatewayJSON struct {
	NetworkID        string            `json:"network_id"`
	ExternalFixedIps []fakeFixedIPJSON `json:"external_fixed_ips"`
	EnableSnat       bool              `json:"enable_snat"`
}

type fakeRouteJSON struct {
	Destination string `json:"destination"`
	Nexthop     string `json:"nexthop"`
}

type fakeRouterJSON struct {
	ID                    string           `json:"id"`
	Name                  string           `json:"name"`
	TenantID              string           `json:"tenant_id"`
	AdminStateUp          bool             `json:"admin_state_up"`
	Status                string           `json:"status"`
	ExternalGatewayInfo   *fakeGatewayJSON `json:"external_gateway_info"`
	Description           string           `json:"description"`
	AvailabilityZones     []string         `json:"availability_zones"`
	AvailabilityZoneHints []any            `json:"availability_zone_hints"`
	Routes                []fakeRouteJSON  `json:"routes"`
	FlavorID              any              `json:"flavor_id"`
	Tags                  []any            `json:"tags"`
	CreatedAt             string           `json:"created_at"`
	UpdatedAt             any              `json:"updated_at"`
	RevisionNumber        int              `json:"revision_number"`
	ProjectID             string           `json:"project_id"`
}

type fakeInterfaceJSON struct {
	ID        string `json:"id"`
	Network   string `json:"network"`
	IPAddress string `json:"ipAddress"`
	Status    string `json:"status"`
	Type      string `json:"type"`
	Cidr      string `json:"cidr"`
	// Omitted on the external gateway entry, exactly as the real listing omits
	// them: it builds that entry without either field.
	NetworkID string `json:"network_id,omitempty"`
	SubnetID  string `json:"subnet_id,omitempty"`
}

type fakeRouterListJSON struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	Snat            bool   `json:"snat"`
	ExternalNetwork string `json:"externalNetwork"`
	Cidr            string `json:"cidr"`
	IsExternal      bool   `json:"isExternal"`
}

type fakeStaticRouteJSON struct {
	DestinationSubnet string `json:"destinationSubnet"`
	NextHop           string `json:"nextHop"`
}

// fakeRouterAPI stands in for the platform's router endpoints. It reproduces,
// deliberately:
//
//   - a create that always attaches a gateway and always answers with the
//     router wrapped in a "router" key;
//   - a new router sitting at DOWN for several reads before reaching ACTIVE;
//   - an update acknowledged while the router still reports its old values;
//   - the listing describing the gateway by network **name** where the details
//     endpoint gives the id;
//   - the interface listing putting a subnet id in the same field the internal
//     entries use for a port id;
//   - the attach endpoint reporting the router rather than the port it made;
//   - `ip_version: 4` making the attach ignore any address asked for;
//   - array fields rejecting null, which is what the real validator does;
//   - static routes applied by rewriting the whole list;
//   - a 404 body with no numeric code in it;
//   - delete answering with a bare status and no body.
type fakeRouterAPI struct {
	mu      sync.Mutex
	routers map[string]*fakeRouter
	seq     int

	networks map[string]fakeNetwork

	creates       int
	updates       int
	deletes       int
	detailsN      int
	attaches      int
	detaches      int
	routeAdds     int
	routeRemovals int
}

func newFakeRouterAPI() *fakeRouterAPI {
	return &fakeRouterAPI{
		routers: map[string]*fakeRouter{},
		networks: map[string]fakeNetwork{
			"net-external": {Name: "public", SubnetID: "subnet-external", Cidr: "203.0.113.0/24"},
			"net-external2": {Name: "public-2", SubnetID: "subnet-external2",
				Cidr: "198.51.100.0/24"},
			"net-private":  {Name: "private", SubnetID: "subnet-private", Cidr: "10.0.10.0/24"},
			"net-private2": {Name: "private-2", SubnetID: "subnet-private2", Cidr: "10.0.20.0/24"},
		},
	}
}

func (f *fakeRouterAPI) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%04d", prefix, f.seq)
}

func (f *fakeRouterAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !acctest.RequireAuth(w, r) {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	path := strings.TrimPrefix(r.URL.Path, "/openstack/routers")
	seg := []string{}
	for _, s := range strings.Split(strings.Trim(path, "/"), "/") {
		if s != "" {
			seg = append(seg, s)
		}
	}

	switch {
	case r.Method == http.MethodGet && len(seg) == 0:
		f.list(w)
	case r.Method == http.MethodPost && len(seg) == 0:
		f.create(w, r)
	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "details":
		f.details(w, seg[0])
	case r.Method == http.MethodPut && len(seg) == 2 && seg[1] == "update":
		f.update(w, r, seg[0])
	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "interfaces":
		f.listInterfaces(w, seg[0])
	case r.Method == http.MethodPut && len(seg) == 2 && seg[1] == "interfaces":
		f.attachInterface(w, r, seg[0])
	case r.Method == http.MethodPut && len(seg) == 2 && seg[1] == "interface":
		f.detachInterface(w, r, seg[0])
	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "staticroutes":
		f.listStaticRoutes(w, seg[0])
	case r.Method == http.MethodPut && len(seg) == 2 && seg[1] == "routes":
		f.addStaticRoute(w, r, seg[0])
	case r.Method == http.MethodPut && len(seg) == 2 && seg[1] == "removeroutes":
		f.removeStaticRoute(w, r, seg[0])
	case r.Method == http.MethodPut && len(seg) == 3 && seg[1] == "routes" && seg[2] == "update":
		// The provider must never call this. The endpoint reads the router's
		// route list, looks for the route being changed, and writes the list
		// back — but when the route is not there the index it found is -1, and
		// assigning at -1 leaves the list untouched. The request is answered
		// 200 having changed nothing. A resource built on it would report
		// success and drift for ever, so both ends of a route are ForceNew and
		// this endpoint is unreachable. A panic is what keeps it that way.
		panic("the provider called PUT /routes/update, which silently succeeds without changing anything")
	case r.Method == http.MethodDelete && len(seg) == 1:
		f.delete(w, seg[0])
	default:
		notFound(w, "NotFound", "no such route")
	}
}

func (f *fakeRouterAPI) get(w http.ResponseWriter, id string) *fakeRouter {
	rt, ok := f.routers[id]
	if !ok || rt.deleted {
		notFound(w, "RouterNotFound", fmt.Sprintf("Router %s could not be found", id))
		return nil
	}
	return rt
}

func (f *fakeRouterAPI) render(rt *fakeRouter) fakeRouterJSON {
	out := fakeRouterJSON{
		ID:                    rt.ID,
		Name:                  rt.Name,
		TenantID:              "tenant-0001",
		AdminStateUp:          rt.AdminStateUp,
		Status:                rt.Status,
		Description:           rt.Description,
		AvailabilityZones:     []string{"nova"},
		AvailabilityZoneHints: []any{},
		Routes:                []fakeRouteJSON{},
		Tags:                  []any{},
		CreatedAt:             fakeRouterTime,
		RevisionNumber:        3,
		ProjectID:             rt.ProjectID,
	}
	for _, route := range rt.Routes {
		out.Routes = append(out.Routes, fakeRouteJSON{Destination: route.Destination, Nexthop: route.NextHop})
	}
	// A router with no gateway reports a JSON null rather than an empty object.
	if rt.ExtNetworkID != "" {
		out.ExternalGatewayInfo = &fakeGatewayJSON{
			NetworkID:        rt.ExtNetworkID,
			ExternalFixedIps: []fakeFixedIPJSON{{SubnetID: rt.ExtSubnetID, IPAddress: rt.ExtIPAddress}},
			EnableSnat:       rt.ExtEnableSnat,
		}
	}
	// Always present: the platform stamps it while the router is still settling
	// after create, so even one nobody has touched reports a real timestamp.
	out.UpdatedAt = rt.UpdatedAt
	return out
}

func (f *fakeRouterAPI) list(w http.ResponseWriter) {
	out := []fakeRouterListJSON{}
	for _, rt := range f.routers {
		if rt.deleted {
			continue
		}
		rt.tick()
		entry := fakeRouterListJSON{
			ID:              rt.ID,
			Name:            rt.Name,
			Status:          rt.Status,
			Snat:            rt.ExtEnableSnat,
			ExternalNetwork: "-",
			IsExternal:      false,
		}
		// The listing reports the external network by name and never sends its
		// id, and a router with no gateway gets a dash and snat false.
		if rt.ExtNetworkID != "" {
			net := f.networks[rt.ExtNetworkID]
			entry.ExternalNetwork = net.Name
			entry.Cidr = net.Cidr
			entry.IsExternal = true
		} else {
			entry.Snat = false
		}
		out = append(out, entry)
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeRouterAPI) create(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		validationError(w, "'name' is required")
		return
	}

	// The create endpoint can attach interfaces itself, and the provider must
	// never ask it to: if any one of them fails the endpoint deletes the router
	// it just made, leaving a create error with no id behind it. Interfaces are
	// dtcloud_router_interface for that reason.
	if _, ok := body["subnetIds"]; ok {
		panic("the provider sent subnetIds to POST /routers; interfaces are a separate resource")
	}

	name, _ := body["name"].(string)
	if name == "" {
		validationError(w, "'name' is required")
		return
	}
	if !noHTML.MatchString(name) {
		// The double quote in this message is rewritten to a single one on the
		// way out, along with every other quote in the body.
		validationError(w, `Invalid characters in 'name'. HTML tags and special characters (<, >, &, ', ') are not allowed.`)
		return
	}
	networkID, _ := body["external_networkId"].(string)
	if networkID == "" {
		validationError(w, "'external_networkId' is required")
		return
	}
	if _, ok := body["external_enableSnat"].(bool); !ok {
		validationError(w, "'external_enableSnat' is required")
		return
	}
	net, ok := f.networks[networkID]
	if !ok {
		notFound(w, "NetworkNotFound", fmt.Sprintf("Network %s could not be found", networkID))
		return
	}

	f.creates++
	id := f.nextID("router")
	rt := &fakeRouter{
		ID:           id,
		Name:         name,
		AdminStateUp: true,
		ProjectID:    "project-0001",
		// Stamped at create, not left null: the platform touches a new router
		// again while it settles, so even one nobody has edited has one.
		UpdatedAt: fakeRouterTime,
		// A new router is not usable yet. It reaches ACTIVE some reads later,
		// which is what the create waiter is for.
		Status:        "DOWN",
		pendingStatus: "ACTIVE",
		statusDelay:   settleReads,
		// The gateway is attached unconditionally: there is no way to ask this
		// endpoint for a router without one.
		ExtNetworkID:  networkID,
		ExtEnableSnat: body["external_enableSnat"].(bool),
		ExtSubnetID:   net.SubnetID,
		// The create request hardcodes ip_version 4 and never accepts an
		// address, so the platform picks one.
		ExtIPAddress: "203.0.113.42",
		Routes:       []fakeRoute{},
		Interfaces:   []*fakeInterface{},
	}
	f.routers[id] = rt

	acctest.WriteJSON(w, http.StatusOK, map[string]any{"router": f.render(rt)})
}

func (f *fakeRouterAPI) details(w http.ResponseWriter, id string) {
	rt := f.get(w, id)
	if rt == nil {
		return
	}
	f.detailsN++
	out := f.render(rt)
	rt.tick()
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeRouterAPI) update(w http.ResponseWriter, r *http.Request, id string) {
	rt := f.get(w, id)
	if rt == nil {
		return
	}

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		validationError(w, "'name' must be a string")
		return
	}

	if raw, ok := body["name"]; ok {
		name, _ := raw.(string)
		if name == "" {
			validationError(w, "'name' is not allowed to be empty")
			return
		}
		if !noHTML.MatchString(name) {
			validationError(w, `Invalid characters in 'name'. HTML tags and special characters (<, >, &, ', ') are not allowed.`)
			return
		}
		// Acknowledged now, visible later. The router never leaves ACTIVE while
		// this is pending, so a wait that watched the status would return
		// having established nothing.
		rt.pendingName = name
		rt.nameDelay = settleReads
	}

	if raw, ok := body["external_gateway_info"]; ok {
		gateway, ok := raw.(map[string]any)
		if !ok {
			validationError(w, "'external_gateway_info' must be an object")
			return
		}
		networkID, _ := gateway["network_id"].(string)
		if networkID == "" {
			validationError(w, "'external_gateway_info.network_id' is required")
			return
		}
		if _, ok := f.networks[networkID]; !ok {
			notFound(w, "NetworkNotFound", fmt.Sprintf("Network %s could not be found", networkID))
			return
		}
		snat, ok := gateway["enable_snat"].(bool)
		if !ok {
			validationError(w, "'external_gateway_info.enable_snat' is required")
			return
		}
		if _, ok := gateway["external_fixed_ips"].([]any); !ok {
			// The endpoint's array validation rejects null outright, so a nil
			// slice serialised as null is refused rather than treated as absent.
			validationError(w, "'external_gateway_info.external_fixed_ips' must be an array")
			return
		}
		rt.pendingNetwork = networkID
		rt.pendingSnat = snat
		rt.gatewayDelay = settleReads
	}

	f.updates++
	rt.UpdatedAt = acctest.FakeCreatedAt
	acctest.WriteJSON(w, http.StatusOK, f.render(rt))
}

func (f *fakeRouterAPI) listInterfaces(w http.ResponseWriter, id string) {
	rt := f.get(w, id)
	if rt == nil {
		return
	}

	out := []fakeInterfaceJSON{}
	// The external gateway comes first and puts a **subnet** id in the same
	// field the internal entries use for a port id.
	if rt.ExtNetworkID != "" {
		net := f.networks[rt.ExtNetworkID]
		out = append(out, fakeInterfaceJSON{
			ID:        rt.ExtSubnetID,
			Network:   net.Name,
			IPAddress: rt.ExtIPAddress,
			Status:    rt.Status,
			Type:      "External gateway",
			Cidr:      net.Cidr,
		})
	}
	for _, iface := range rt.Interfaces {
		net := f.networks[iface.NetworkID]
		out = append(out, fakeInterfaceJSON{
			ID:        iface.PortID,
			Network:   net.Name,
			IPAddress: iface.IPAddress,
			Status:    "ACTIVE",
			Type:      "Internal interface",
			Cidr:      net.Cidr,
			NetworkID: iface.NetworkID,
			SubnetID:  iface.SubnetID,
		})
	}
	rt.tick()
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeRouterAPI) attachInterface(w http.ResponseWriter, r *http.Request, id string) {
	rt := f.get(w, id)
	if rt == nil {
		return
	}

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		validationError(w, "'network_id' is required")
		return
	}

	networkID, _ := body["network_id"].(string)
	if networkID == "" {
		validationError(w, "'network_id' is required")
		return
	}
	net, ok := f.networks[networkID]
	if !ok {
		notFound(w, "NetworkNotFound", fmt.Sprintf("Network %s could not be found", networkID))
		return
	}
	if _, ok := body["port_security_enabled"].(bool); !ok {
		validationError(w, "'port_security_enabled' is required")
		return
	}
	fixedIPs, ok := body["fixed_ips"].([]any)
	if !ok {
		validationError(w, "'fixed_ips' must be an array")
		return
	}

	// The endpoint branches on the first fixed IP. Declaring IPv4 attaches the
	// network's first subnet and discards any address sent alongside; sending
	// no version makes it create a port carrying exactly the address given.
	address := ""
	if len(fixedIPs) > 0 {
		first, _ := fixedIPs[0].(map[string]any)
		version, hasVersion := first["ip_version"].(float64)
		if !hasVersion || version != 4 {
			address, _ = first["ip_address"].(string)
		}
	}
	if address == "" {
		address = "10.0.10.1"
	}

	for _, existing := range rt.Interfaces {
		if existing.NetworkID == networkID {
			acctest.WriteJSON(w, http.StatusConflict, map[string]any{
				"error": map[string]any{
					"NeutronError": map[string]any{
						"type":    "BadRequest",
						"message": fmt.Sprintf("Router already has a port on network %s", networkID),
						"detail":  "",
					},
				},
				"code": "SERVER_ERROR",
			})
			return
		}
	}

	f.attaches++
	next := append([]*fakeInterface{}, rt.Interfaces...)
	next = append(next, &fakeInterface{
		PortID:    f.nextID("port"),
		NetworkID: networkID,
		SubnetID:  net.SubnetID,
		IPAddress: address,
	})
	rt.pendingIfaces = next
	rt.ifacesDelay = settleReads

	// The attach endpoint answers with the router, not with the port it just
	// created — which is why the provider has to diff the interface list to
	// learn the port id.
	acctest.WriteJSON(w, http.StatusOK, f.render(rt))
}

func (f *fakeRouterAPI) detachInterface(w http.ResponseWriter, r *http.Request, id string) {
	rt := f.get(w, id)
	if rt == nil {
		return
	}

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		validationError(w, "'port_id' is required")
		return
	}
	portID, _ := body["port_id"].(string)
	if portID == "" {
		validationError(w, "'port_id' is required")
		return
	}

	// An interface a static route points through cannot be detached: the route
	// needs it to stay reachable. Observed live, and the reason a router's
	// routes have to go before its interfaces.
	for _, iface := range rt.Interfaces {
		if iface.PortID != portID {
			continue
		}
		for _, route := range rt.Routes {
			if subnetOf(route.NextHop) == subnetOf(iface.IPAddress) {
				acctest.WriteJSON(w, http.StatusConflict, map[string]any{
					"error": map[string]any{
						"NeutronError": map[string]any{
							"type": "RouterInterfaceInUseByRoute",
							"message": fmt.Sprintf(
								"Router interface for subnet %s on router %s cannot be deleted, as it is required by one or more routes.",
								iface.SubnetID, rt.ID),
							"detail": "",
						},
					},
					"code": "SERVER_ERROR",
				})
				return
			}
		}
	}

	f.detaches++
	next := []*fakeInterface{}
	for _, iface := range rt.Interfaces {
		if iface.PortID != portID {
			next = append(next, iface)
		}
	}
	rt.pendingIfaces = next
	rt.ifacesDelay = settleReads

	acctest.WriteJSON(w, http.StatusOK, map[string]any{"id": rt.ID, "port_id": portID})
}

func (f *fakeRouterAPI) listStaticRoutes(w http.ResponseWriter, id string) {
	rt := f.get(w, id)
	if rt == nil {
		return
	}
	out := []fakeStaticRouteJSON{}
	for _, route := range rt.Routes {
		// The endpoint renames both fields on the way out.
		out = append(out, fakeStaticRouteJSON{DestinationSubnet: route.Destination, NextHop: route.NextHop})
	}
	rt.tick()
	acctest.WriteJSON(w, http.StatusOK, out)
}

// subnetOf reduces an address to its first three octets, which is enough to
// decide whether a next hop sits on an interface's network in these fixtures.
func subnetOf(ip string) string {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return ip
	}
	return strings.Join(parts[:3], ".")
}

// readRouteBody applies the validation the two static-route endpoints share:
// the destination must carry a prefix length and the next hop must not.
func readRouteBody(w http.ResponseWriter, r *http.Request) (fakeRoute, bool) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		validationError(w, "'destination' is required")
		return fakeRoute{}, false
	}
	destination, _ := body["destination"].(string)
	nexthop, _ := body["nexthop"].(string)
	if _, _, err := net.ParseCIDR(destination); err != nil {
		validationError(w, "'destination' must be a valid ip address with a required CIDR")
		return fakeRoute{}, false
	}
	// A next hop carrying a prefix length is **not** caught by the request
	// validation — that rule allows one — and is refused further down as a bad
	// request, with a different body and a different status. The provider
	// rejects it during plan so neither is ever seen in practice, but a fake
	// answering 406 here would describe a rule the API does not have.
	if strings.Contains(nexthop, "/") {
		badRequest(w, fmt.Sprintf("Invalid input for routes. Reason: '%s' is not a valid IP address.", nexthop))
		return fakeRoute{}, false
	}
	ip := net.ParseIP(nexthop)
	if ip == nil || ip.To4() == nil {
		validationError(w, "'nexthop' must be a valid ip address of one of the following versions [ipv4] with a optional CIDR")
		return fakeRoute{}, false
	}
	return fakeRoute{Destination: destination, NextHop: nexthop}, true
}

func (f *fakeRouterAPI) addStaticRoute(w http.ResponseWriter, r *http.Request, id string) {
	rt := f.get(w, id)
	if rt == nil {
		return
	}
	route, ok := readRouteBody(w, r)
	if !ok {
		return
	}

	f.routeAdds++
	// Read, edit, write back the whole list — which is what makes two of these
	// at once overwrite each other, and why the provider serialises them.
	next := append([]fakeRoute{}, rt.Routes...)
	next = append(next, route)
	rt.pendingRoutes = next
	rt.routesDelay = settleReads

	acctest.WriteJSON(w, http.StatusOK, f.render(rt))
}

func (f *fakeRouterAPI) removeStaticRoute(w http.ResponseWriter, r *http.Request, id string) {
	rt := f.get(w, id)
	if rt == nil {
		return
	}
	route, ok := readRouteBody(w, r)
	if !ok {
		return
	}

	f.routeRemovals++
	next := []fakeRoute{}
	for _, existing := range rt.Routes {
		if existing != route {
			next = append(next, existing)
		}
	}
	rt.pendingRoutes = next
	rt.routesDelay = settleReads

	acctest.WriteJSON(w, http.StatusOK, f.render(rt))
}

func (f *fakeRouterAPI) delete(w http.ResponseWriter, id string) {
	rt := f.get(w, id)
	if rt == nil {
		return
	}
	f.deletes++
	// Deleting a router detaches its interfaces first, and swallows any error
	// from doing so.
	rt.pendingIfaces = []*fakeInterface{}
	rt.ifacesDelay = 1
	rt.deleteDelay = settleReads
	// A bare status, no body.
	w.WriteHeader(http.StatusOK)
}
