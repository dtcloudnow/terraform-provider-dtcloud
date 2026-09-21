package loadbalancer_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
)

// fakeLB models the little tree the API exposes: a load balancer owns
// listeners, a listener owns a pool, a pool owns members and one health monitor.
type fakeLB struct {
	ID          string
	Name        string
	Description string
	Enabled     bool
	FlavorID    string
	deleted     bool
	// settlePolls delays the move to ACTIVE, so the waiters are exercised.
	settlePolls int
	// dirty is set by every change and cleared by a details read. It stands in for
	// the per-load-balancer lock: one change at a time, anything else refused. A
	// fake cannot reproduce a race, but it can enforce the discipline that avoids
	// one — every change must be preceded by a read showing the LB settled.
	dirty bool

	listeners []*fakeListener
	pools     []*fakePool
	monitors  []*fakeMonitor
	bps       []*fakeBP
}

type fakeListener struct {
	ID           string
	Name         string
	Description  string
	Protocol     string
	Port         int
	AdminStateUp bool
	ConnLimit    int
	DefaultPool  string
	AllowedCIDRs []string
}

type fakePool struct {
	ID         string
	ListenerID string
	Protocol   string
	Port       int
	Algorithm  string
	Sticky     bool
	MonitorID  string
	members    []*fakeMember
}

type fakeMember struct {
	ID string
	// Name is what the caller sent. It is stored and never reported: the list
	// answers with the name of the VM behind the member instead.
	Name    string
	VMName  string
	Address string
	State   string
}

// candidateVMs is the fake's inventory of VMs, shared by the candidate lookup
// and by the member list, which names members after them.
var candidateVMs = map[string]struct {
	Name string
	IPs  []string
}{
	"vm-1": {Name: "web-01-vm", IPs: []string{"10.0.0.11"}},
	"vm-2": {Name: "web-02-vm", IPs: []string{"10.0.0.12", "10.0.0.13"}},
}

type fakeMonitor struct {
	ID             string
	PoolID         string
	Type           string
	Delay          int
	Timeout        int
	MaxRetries     int
	MaxRetriesDown int
	URLPath        string
}

type fakeBP struct {
	ID          string
	ListenerID  string
	MonitorID   string
	LBProtocol  string
	LBPort      int
	BEProtocol  string
	BEPort      int
	MemberCount int

	// Reported only by the details endpoint, which is how the fake proves the read
	// uses it: a list-only read would leave these empty.
	Algorithm     string
	StickySession bool
	Type          string
	URLPath       string
	Interval      int
	Timeout       int
	Healthy       int
	Unhealthy     int
}

// fakeLBAPI stands in for the load balancer routes.
type fakeLBAPI struct {
	mu  sync.Mutex
	lbs map[string]*fakeLB
	seq int

	// buildPolls is how many reads report PENDING_CREATE before ACTIVE.
	buildPolls int

	creates int
	updates int
}

func newFakeLBAPI() *fakeLBAPI {
	return &fakeLBAPI{lbs: map[string]*fakeLB{}, buildPolls: 1}
}

func (f *fakeLBAPI) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%04d", prefix, f.seq)
}

func (f *fakeLBAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !acctest.RequireAuth(w, r) {
		return
	}

	// Not part of this service, but answered so a stray call does not fail a test.
	if r.Method == http.MethodGet && r.URL.Path == "/openstack/flavors/loadbalancer" {
		acctest.WriteJSON(w, http.StatusOK, []map[string]any{{"id": "lb-flavor", "name": "lb-small"}})
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/openstack/loadbalancers")
	seg := strings.Split(strings.Trim(path, "/"), "/")
	if len(seg) == 1 && seg[0] == "" {
		seg = nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case r.Method == http.MethodPost && len(seg) == 0:
		f.createLB(w, r)
	case r.Method == http.MethodGet && len(seg) == 0:
		f.listLBs(w)
	case r.Method == http.MethodGet && len(seg) == 2 && seg[0] == "vms":
		f.listCandidateVMs(w, seg[1])
	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "details":
		f.lbDetails(w, seg[0])
	case r.Method == http.MethodPut && len(seg) == 1:
		f.updateLB(w, r, seg[0])
	case r.Method == http.MethodDelete && len(seg) == 1:
		f.deleteLB(w, seg[0])

	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "listeners":
		f.listListeners(w, seg[0])
	case r.Method == http.MethodPost && len(seg) == 2 && seg[1] == "listeners":
		f.createListener(w, r, seg[0])
	case r.Method == http.MethodPut && len(seg) == 3 && seg[1] == "listeners":
		f.updateListener(w, r, seg[0], seg[2])
	case r.Method == http.MethodDelete && len(seg) == 3 && seg[1] == "listeners":
		f.deleteListener(w, seg[0], seg[2])

	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "pools":
		f.listPools(w, seg[0])
	case r.Method == http.MethodPost && len(seg) == 2 && seg[1] == "pools":
		f.createPool(w, r, seg[0])
	case r.Method == http.MethodPut && len(seg) == 3 && seg[1] == "pools":
		f.updatePool(w, r, seg[0], seg[2])
	case r.Method == http.MethodDelete && len(seg) == 3 && seg[1] == "pools":
		f.deletePool(w, seg[0], seg[2])

	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "hm":
		f.listMonitors(w, seg[0])
	case r.Method == http.MethodPost && len(seg) == 2 && seg[1] == "hm":
		f.createMonitor(w, r, seg[0])
	case r.Method == http.MethodPut && len(seg) == 3 && seg[1] == "hm":
		f.updateMonitor(w, r, seg[0], seg[2])
	case r.Method == http.MethodDelete && len(seg) == 3 && seg[1] == "hm":
		f.deleteMonitor(w, seg[0], seg[2])

	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "bp":
		f.listBPs(w, seg[0])
	case r.Method == http.MethodPost && len(seg) == 2 && seg[1] == "bp":
		f.createBP(w, r, seg[0])
	case r.Method == http.MethodGet && len(seg) == 4 && seg[1] == "bp" && seg[3] == "details":
		f.bpDetails(w, seg[0], seg[2])
	case r.Method == http.MethodDelete && len(seg) == 3 && seg[1] == "bp":
		f.deleteBP(w, seg[0], seg[2])

	// Members hang off /{lb}/{pool}/members, with no literal segment in between.
	case r.Method == http.MethodGet && len(seg) == 3 && seg[2] == "members":
		f.listMembers(w, seg[0], seg[1])
	case r.Method == http.MethodPost && len(seg) == 3 && seg[2] == "members":
		f.createMember(w, r, seg[0], seg[1])
	case r.Method == http.MethodPut && len(seg) == 4 && seg[2] == "members":
		f.updateMember(w, r, seg[0], seg[1], seg[3])
	case r.Method == http.MethodDelete && len(seg) == 4 && seg[2] == "members":
		f.deleteMember(w, seg[0], seg[1], seg[3])

	default:
		acctest.NotFound(w, "no such route")
	}
}

func (f *fakeLBAPI) lb(w http.ResponseWriter, id string) *fakeLB {
	lb, ok := f.lbs[id]
	if !ok || lb.deleted {
		acctest.NotFound(w, "load balancer not found")
		return nil
	}
	return lb
}

// mutableLB is lb() for the handlers that change something. It enforces the
// lock described on fakeLB.dirty.
func (f *fakeLBAPI) mutableLB(w http.ResponseWriter, id string) *fakeLB {
	lb := f.lb(w, id)
	if lb == nil {
		return nil
	}
	if lb.dirty {
		lb.dirty = false
		acctest.WriteJSON(w, http.StatusConflict, map[string]any{
			"errorMessage": fmt.Sprintf("Load Balancer %s is immutable and cannot be updated.", id)})
		return nil
	}
	lb.dirty = true
	return lb
}

func (f *fakeLBAPI) createLB(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		FlavorID     string `json:"flavorId"`
		NetworkType  string `json:"networkType"`
		VipNetworkID string `json:"vip_network_id"`
		VipSubnetID  string `json:"vip_subnet_id"`
		VipPortID    string `json:"vip_port_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "bad body"})
		return
	}
	if body.Name == "" || body.FlavorID == "" || body.NetworkType == "" {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "name, flavorId and networkType are required"})
		return
	}
	// Exactly one VIP anchor.
	anchors := 0
	for _, v := range []string{body.VipNetworkID, body.VipSubnetID, body.VipPortID} {
		if v != "" {
			anchors++
		}
	}
	if anchors != 1 {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"errorMessage": "exactly one of vip_network_id, vip_subnet_id, vip_port_id is required"})
		return
	}

	id := f.nextID("lb")
	f.lbs[id] = &fakeLB{
		ID: id, Name: body.Name, Description: body.Description,
		Enabled: true, FlavorID: body.FlavorID, settlePolls: f.buildPolls,
	}
	f.creates++
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"loadbalancer": map[string]any{"id": id}})
}

// The statuses the API really emits: one title-case word folded from the
// provisioning and operating statuses, so "ACTIVE" never reaches a client.
func (f *fakeLBAPI) lbStatus(lb *fakeLB) string {
	// "Disabled" means administratively disabled, not "no listener yet": a load
	// balancer one second old with nothing attached already reports "Active".
	if !lb.Enabled {
		return "Disabled"
	}
	return "Active"
}

func (f *fakeLBAPI) lbBody(lb *fakeLB) map[string]any {
	status := f.lbStatus(lb)
	// Only the details endpoint burns a poll: that is the one the waiter uses.
	if lb.settlePolls > 0 {
		lb.settlePolls--
		status = "Creating"
	}
	return map[string]any{
		"id": lb.ID, "name": lb.Name, "description": lb.Description,
		"status": status, "floatingIp": "", "flavorName": "lb-small",
		"highAvailability": "enabled", "balancingPools": len(lb.bps),
		"membersState": []string{}, "membersTotal": 0,
		"createdOn": acctest.FakeCreatedAt,
		"network":   map[string]any{"id": "net-1", "name": "lb-net", "ip": "10.0.0.5"},
	}
}

func (f *fakeLBAPI) lbDetails(w http.ResponseWriter, id string) {
	lb := f.lb(w, id)
	if lb == nil {
		return
	}
	lb.dirty = false
	acctest.WriteJSON(w, http.StatusOK, f.lbBody(lb))
}

func (f *fakeLBAPI) listLBs(w http.ResponseWriter) {
	out := []map[string]any{}
	for _, lb := range f.lbs {
		if lb.deleted {
			continue
		}
		out = append(out, map[string]any{
			"id": lb.ID, "name": lb.Name, "status": f.lbStatus(lb),
			"ipAddress": "10.0.0.5", "floatingIp": "", "membersTotal": 0,
			"membersState": []string{}, "portId": "port-1", "flavorName": "lb-small",
			"network": map[string]any{"id": "net-1", "name": "lb-net", "ip": "10.0.0.5"},
		})
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeLBAPI) updateLB(w http.ResponseWriter, r *http.Request, id string) {
	lb := f.mutableLB(w, id)
	if lb == nil {
		return
	}
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Enabled     *bool  `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Name != "" {
		lb.Name = body.Name
	}
	lb.Description = body.Description
	if body.Enabled != nil {
		lb.Enabled = *body.Enabled
	}
	f.updates++
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "updated"})
}

func (f *fakeLBAPI) deleteLB(w http.ResponseWriter, id string) {
	lb := f.mutableLB(w, id)
	if lb == nil {
		return
	}
	lb.deleted = true
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}

// The addresses come back as `ips`, a list built from every port the VM holds
// on the network.
func (f *fakeLBAPI) listCandidateVMs(w http.ResponseWriter, networkID string) {
	out := []map[string]any{}
	for _, id := range []string{"vm-1", "vm-2"} {
		out = append(out, map[string]any{"id": id, "name": candidateVMs[id].Name, "ips": candidateVMs[id].IPs})
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeLBAPI) listListeners(w http.ResponseWriter, lbID string) {
	lb := f.lb(w, lbID)
	if lb == nil {
		return
	}
	out := []map[string]any{}
	for _, l := range lb.listeners {
		out = append(out, map[string]any{
			"id": l.ID, "name": l.Name, "description": l.Description,
			"protocol": l.Protocol, "protocol_port": l.Port,
			"admin_state_up": l.AdminStateUp, "connection_limit": l.ConnLimit,
			"default_pool_id": l.DefaultPool, "allowed_cidrs": l.AllowedCIDRs,
			"provisioning_status": "ACTIVE", "operating_status": "ONLINE",
			"created_at": acctest.FakeCreatedAt, "updated_at": acctest.FakeCreatedAt,
			"insert_headers": map[string]any{},
		})
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeLBAPI) createListener(w http.ResponseWriter, r *http.Request, lbID string) {
	lb := f.mutableLB(w, lbID)
	if lb == nil {
		return
	}
	var body struct {
		Name           string `json:"name"`
		Description    string `json:"description"`
		LBProtocol     string `json:"lb_protocol"`
		LBProtocolPort int    `json:"lb_protocol_port"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.LBProtocol == "" || body.LBProtocolPort == 0 {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "lb_protocol and lb_protocol_port are required"})
		return
	}
	lb.listeners = append(lb.listeners, &fakeListener{
		ID: f.nextID("listener"), Name: body.Name, Description: body.Description,
		Protocol: body.LBProtocol, Port: body.LBProtocolPort, AdminStateUp: true,
	})
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "created"})
}

func (f *fakeLBAPI) updateListener(w http.ResponseWriter, r *http.Request, lbID, listenerID string) {
	lb := f.mutableLB(w, lbID)
	if lb == nil {
		return
	}
	var body struct {
		Name            string   `json:"name"`
		Description     string   `json:"description"`
		AdminStateUp    *bool    `json:"admin_state_up"`
		ConnectionLimit *int     `json:"connection_limit"`
		DefaultPoolID   string   `json:"default_pool_id"`
		AllowedCIDRs    []string `json:"allowed_cidrs"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	for _, l := range lb.listeners {
		if l.ID != listenerID {
			continue
		}
		if body.Name != "" {
			l.Name = body.Name
		}
		l.Description = body.Description
		if body.AdminStateUp != nil {
			l.AdminStateUp = *body.AdminStateUp
		}
		if body.ConnectionLimit != nil {
			l.ConnLimit = *body.ConnectionLimit
		}
		if body.DefaultPoolID != "" {
			l.DefaultPool = body.DefaultPoolID
		}
		l.AllowedCIDRs = body.AllowedCIDRs
		acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "updated"})
		return
	}
	acctest.NotFound(w, "listener not found")
}

func (f *fakeLBAPI) deleteListener(w http.ResponseWriter, lbID, listenerID string) {
	lb := f.mutableLB(w, lbID)
	if lb == nil {
		return
	}
	kept := lb.listeners[:0]
	for _, l := range lb.listeners {
		if l.ID != listenerID {
			kept = append(kept, l)
		}
	}
	lb.listeners = kept
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}

func (f *fakeLBAPI) listPools(w http.ResponseWriter, lbID string) {
	lb := f.lb(w, lbID)
	if lb == nil {
		return
	}
	out := []map[string]any{}
	for _, p := range lb.pools {
		members := []map[string]any{}
		for _, m := range p.members {
			members = append(members, map[string]any{"id": m.ID})
		}
		out = append(out, map[string]any{
			"id": p.ID, "name": "pool-" + p.ID, "description": "",
			"provisioning_status": "ACTIVE", "operating_status": "ONLINE",
			"admin_state_up": true, "protocol": p.Protocol,
			"default_protocol_port": p.Port, "lb_algorithm": p.Algorithm,
			"listeners":  []map[string]any{{"id": p.ListenerID}},
			"members":    members,
			"created_at": acctest.FakeCreatedAt, "updated_at": acctest.FakeCreatedAt,
			"healthmonitor_id": p.MonitorID,
		})
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeLBAPI) createPool(w http.ResponseWriter, r *http.Request, lbID string) {
	lb := f.mutableLB(w, lbID)
	if lb == nil {
		return
	}
	var body struct {
		ListenerID          string `json:"listener_id"`
		BackendProtocol     string `json:"backend_protocol"`
		BackendProtocolPort int    `json:"backend_protocol_port"`
		LBAlgorithm         string `json:"lb_algorithm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.ListenerID == "" || body.LBAlgorithm == "" {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "listener_id and lb_algorithm are required"})
		return
	}
	lb.pools = append(lb.pools, &fakePool{
		ID: f.nextID("pool"), ListenerID: body.ListenerID,
		Protocol: body.BackendProtocol, Port: body.BackendProtocolPort,
		Algorithm: body.LBAlgorithm,
	})
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "created"})
}

func (f *fakeLBAPI) updatePool(w http.ResponseWriter, r *http.Request, lbID, poolID string) {
	lb := f.mutableLB(w, lbID)
	if lb == nil {
		return
	}
	var body struct {
		LBAlgorithm   string `json:"lb_algorithm"`
		StickySession *bool  `json:"sticky_session"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	for _, p := range lb.pools {
		if p.ID != poolID {
			continue
		}
		if body.LBAlgorithm != "" {
			p.Algorithm = body.LBAlgorithm
		}
		if body.StickySession != nil {
			p.Sticky = *body.StickySession
		}
		acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "updated"})
		return
	}
	acctest.NotFound(w, "pool not found")
}

func (f *fakeLBAPI) deletePool(w http.ResponseWriter, lbID, poolID string) {
	lb := f.mutableLB(w, lbID)
	if lb == nil {
		return
	}
	kept := lb.pools[:0]
	for _, p := range lb.pools {
		if p.ID != poolID {
			kept = append(kept, p)
		}
	}
	lb.pools = kept
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}

func (f *fakeLBAPI) findPool(lb *fakeLB, poolID string) *fakePool {
	for _, p := range lb.pools {
		if p.ID == poolID {
			return p
		}
	}
	return nil
}

func (f *fakeLBAPI) listMembers(w http.ResponseWriter, lbID, poolID string) {
	lb := f.lb(w, lbID)
	if lb == nil {
		return
	}
	p := f.findPool(lb, poolID)
	if p == nil {
		acctest.NotFound(w, "pool not found")
		return
	}
	out := []map[string]any{}
	for _, m := range p.members {
		// m.Name is deliberately not sent: the member name is overwritten with the
		// VM's before the answer comes back.
		out = append(out, map[string]any{"id": m.ID, "name": m.VMName, "state": m.State, "ipAddress": m.Address})
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeLBAPI) createMember(w http.ResponseWriter, r *http.Request, lbID, poolID string) {
	lb := f.mutableLB(w, lbID)
	if lb == nil {
		return
	}
	p := f.findPool(lb, poolID)
	if p == nil {
		acctest.NotFound(w, "pool not found")
		return
	}
	var body struct {
		Name            string `json:"name"`
		Address         string `json:"address"`
		ComputeServerID string `json:"compute_server_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Address == "" || body.ComputeServerID == "" {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "address and compute_server_id are required"})
		return
	}
	vmName := "Not now"
	if vm, ok := candidateVMs[body.ComputeServerID]; ok {
		vmName = vm.Name
	}
	p.members = append(p.members, &fakeMember{
		ID: f.nextID("member"), Name: body.Name, VMName: vmName,
		Address: body.Address, State: "ONLINE",
	})
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "created"})
}

func (f *fakeLBAPI) updateMember(w http.ResponseWriter, r *http.Request, lbID, poolID, memberID string) {
	lb := f.mutableLB(w, lbID)
	if lb == nil {
		return
	}
	p := f.findPool(lb, poolID)
	if p == nil {
		acctest.NotFound(w, "pool not found")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	for _, m := range p.members {
		if m.ID == memberID {
			if body.Name != "" {
				m.Name = body.Name
			}
			acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "updated"})
			return
		}
	}
	acctest.NotFound(w, "member not found")
}

func (f *fakeLBAPI) deleteMember(w http.ResponseWriter, lbID, poolID, memberID string) {
	lb := f.mutableLB(w, lbID)
	if lb == nil {
		return
	}
	p := f.findPool(lb, poolID)
	if p == nil {
		acctest.NotFound(w, "pool not found")
		return
	}
	kept := p.members[:0]
	for _, m := range p.members {
		if m.ID != memberID {
			kept = append(kept, m)
		}
	}
	p.members = kept
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}

func (f *fakeLBAPI) listMonitors(w http.ResponseWriter, lbID string) {
	lb := f.lb(w, lbID)
	if lb == nil {
		return
	}
	out := []map[string]any{}
	for _, h := range lb.monitors {
		out = append(out, map[string]any{
			"id": h.ID, "name": "hm-" + h.ID, "type": h.Type,
			"delay": h.Delay, "timeout": h.Timeout,
			"max_retries": h.MaxRetries, "max_retries_down": h.MaxRetriesDown,
			"http_method": "GET", "url_path": h.URLPath, "expected_codes": "200",
			"admin_state_up": true, "pools": []map[string]any{{"id": h.PoolID}},
			"provisioning_status": "ACTIVE", "operating_status": "ONLINE",
			"created_at": acctest.FakeCreatedAt, "updated_at": acctest.FakeCreatedAt,
		})
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"healthmonitors": out})
}

func (f *fakeLBAPI) createMonitor(w http.ResponseWriter, r *http.Request, lbID string) {
	lb := f.mutableLB(w, lbID)
	if lb == nil {
		return
	}
	var body struct {
		PoolID             string  `json:"pool_id"`
		Interval           int     `json:"interval"`
		Timeout            int     `json:"timeout"`
		HealthyThreshold   int     `json:"healthy_threshold"`
		UnhealthyThreshold int     `json:"unhealthy_threshold"`
		Type               string  `json:"type"`
		URLPath            *string `json:"url_path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.PoolID == "" || body.Type == "" {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "pool_id and type are required"})
		return
	}
	// A pool can carry only one monitor; the API rejects a second.
	for _, h := range lb.monitors {
		if h.PoolID == body.PoolID {
			acctest.WriteJSON(w, http.StatusConflict, map[string]any{"errorMessage": "pool already has a health monitor"})
			return
		}
	}
	path := ""
	if body.URLPath != nil {
		path = *body.URLPath
	}
	h := &fakeMonitor{
		ID: f.nextID("hm"), PoolID: body.PoolID, Type: body.Type,
		Delay: body.Interval, Timeout: body.Timeout,
		MaxRetries: body.HealthyThreshold, MaxRetriesDown: body.UnhealthyThreshold,
		URLPath: path,
	}
	lb.monitors = append(lb.monitors, h)
	if p := f.findPool(lb, body.PoolID); p != nil {
		p.MonitorID = h.ID
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "created"})
}

func (f *fakeLBAPI) updateMonitor(w http.ResponseWriter, r *http.Request, lbID, hmID string) {
	lb := f.mutableLB(w, lbID)
	if lb == nil {
		return
	}
	var body struct {
		Interval           *int    `json:"interval"`
		Timeout            *int    `json:"timeout"`
		HealthyThreshold   *int    `json:"healthy_threshold"`
		UnhealthyThreshold *int    `json:"unhealthy_threshold"`
		URLPath            *string `json:"url_path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	for _, h := range lb.monitors {
		if h.ID != hmID {
			continue
		}
		if body.Interval != nil {
			h.Delay = *body.Interval
		}
		if body.Timeout != nil {
			h.Timeout = *body.Timeout
		}
		if body.HealthyThreshold != nil {
			h.MaxRetries = *body.HealthyThreshold
		}
		if body.UnhealthyThreshold != nil {
			h.MaxRetriesDown = *body.UnhealthyThreshold
		}
		if body.URLPath != nil {
			h.URLPath = *body.URLPath
		}
		acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "updated"})
		return
	}
	acctest.NotFound(w, "health monitor not found")
}

func (f *fakeLBAPI) deleteMonitor(w http.ResponseWriter, lbID, hmID string) {
	lb := f.mutableLB(w, lbID)
	if lb == nil {
		return
	}
	kept := lb.monitors[:0]
	for _, h := range lb.monitors {
		if h.ID != hmID {
			kept = append(kept, h)
		} else if p := f.findPool(lb, h.PoolID); p != nil {
			p.MonitorID = ""
		}
	}
	lb.monitors = kept
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}

func (f *fakeLBAPI) listBPs(w http.ResponseWriter, lbID string) {
	lb := f.lb(w, lbID)
	if lb == nil {
		return
	}
	out := []map[string]any{}
	for _, b := range lb.bps {
		out = append(out, map[string]any{
			"id": b.ID, "listenerId": b.ListenerID, "healthMonitorId": b.MonitorID,
			"lbProtocol": b.LBProtocol, "lbProtocolPort": b.LBPort,
			"backEndProtocol": b.BEProtocol, "backEndProtocolPort": b.BEPort,
			"status": "Active",
		})
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeLBAPI) createBP(w http.ResponseWriter, r *http.Request, lbID string) {
	lb := f.mutableLB(w, lbID)
	if lb == nil {
		return
	}
	var body struct {
		LBProtocol      string `json:"lb_protocol"`
		LBPort          int    `json:"lb_port"`
		LBAlgorithm     string `json:"lb_algorithm"`
		StickySession   bool   `json:"sticky_session"`
		BackendProtocol string `json:"backend_protocol"`
		BackendPort     int    `json:"backend_protocol_port"`
		Type            string `json:"type"`
		URLPath         string `json:"url_path"`
		Interval        int    `json:"interval"`
		Timeout         int    `json:"timeout"`
		Healthy         int    `json:"healthy_threshold"`
		Unhealthy       int    `json:"unhealthy_threshold"`
		Members         []struct {
			Address string `json:"address"`
		} `json:"members"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.LBProtocol == "" || body.Type == "" {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "lb_protocol and type are required"})
		return
	}
	lb.bps = append(lb.bps, &fakeBP{
		ID: f.nextID("bp"), ListenerID: f.nextID("listener"), MonitorID: f.nextID("hm"),
		LBProtocol: body.LBProtocol, LBPort: body.LBPort,
		BEProtocol: body.BackendProtocol, BEPort: body.BackendPort,
		MemberCount:   len(body.Members),
		Algorithm:     body.LBAlgorithm,
		StickySession: body.StickySession,
		Type:          body.Type, URLPath: body.URLPath,
		Interval: body.Interval, Timeout: body.Timeout,
		Healthy: body.Healthy, Unhealthy: body.Unhealthy,
	})
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "created"})
}

// bpDetails answers the way the real endpoint does, which is not the way it was
// asked: the algorithm comes back as a display string and sticky_session as a
// word, so feeding these straight into state would leave a permanent diff.
func (f *fakeLBAPI) bpDetails(w http.ResponseWriter, lbID, bpID string) {
	lb := f.lb(w, lbID)
	if lb == nil {
		return
	}
	for _, b := range lb.bps {
		if b.ID != bpID {
			continue
		}
		display := map[string]string{
			"ROUND_ROBIN":       "Round robin",
			"SOURCE_IP":         "Source IP",
			"LEAST_CONNECTIONS": "Least connections",
		}[b.Algorithm]
		sticky := "Disabled"
		if b.StickySession {
			sticky = "Enabled"
		}
		acctest.WriteJSON(w, http.StatusOK, map[string]any{
			"id": b.ID, "status": f.lbStatus(lb),
			"balancingAlgorithm": display, "stickySession": sticky,
			"healthMonitorId": b.MonitorID, "listenerId": b.ListenerID,
			"protocol": b.Type, "urlPath": b.URLPath,
			"interval": b.Interval, "timeout": b.Timeout,
			"healthyThreshold": b.Healthy, "unHealthyThreshold": b.Unhealthy,
			"membersTotal": b.MemberCount, "membersState": []string{},
			"lbProtocol": b.LBProtocol, "lbProtocolPort": b.LBPort,
			"backEndProtocol": b.BEProtocol, "backEndProtocolPort": b.BEPort,
			// -1 is "no limit" and is what an unset connection_limit becomes. The read
			// has to leave it out of state, not write -1 in.
			"connectionLimit": -1, "allowedCidrs": nil,
			"insertHeaders":     map[string]any{},
			"timeoutClientData": 50, "timeoutMemberConnect": 5, "timeoutMemberData": 50,
		})
		return
	}
	acctest.NotFound(w, "balancing pool not found")
}

func (f *fakeLBAPI) deleteBP(w http.ResponseWriter, lbID, bpID string) {
	lb := f.mutableLB(w, lbID)
	if lb == nil {
		return
	}
	kept := lb.bps[:0]
	for _, b := range lb.bps {
		if b.ID != bpID {
			kept = append(kept, b)
		}
	}
	lb.bps = kept
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}
