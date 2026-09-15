package vm_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
)

const (
	fakeVMID      = "9f1c2b3a-0000-4a1b-8c2d-1234567890ab"
	fakeVMImage   = "ubuntu-22.04"
	fakeVMImageID = "img-0001"
	fakeVMFault   = "Exceeded maximum number of retries. Exhausted all hosts."
	fakeVMOsType  = "linux"
	fakeVMFlavor  = "tiny"
	fakeVMKeyName = "tf-acc-key"
)

// fakeVM is one instance held by the fake API.
type fakeVM struct {
	ID     string
	Name   string
	Flavor string
	// pollsLeft counts how many more reads report BUILD before the VM settles.
	pollsLeft     int
	deleted       bool
	stopped       bool
	shelved       bool
	hotPlug       bool
	before        string
	pendingFlavor string
	// settlePolls delays a power transition by a couple of reads: a VM asked to
	// stop stays ACTIVE for a while.
	settlePolls int
	// verifyPolls counts how many reads report VERIFY_RESIZE after a resize. The
	// platform parks the VM there and settles it on its own, with no confirm step.
	verifyPolls int

	ifaces  []*fakeIface
	volumes []*fakeVol

	// userData is what create sent, after the base64 the API insists on has been
	// decoded — so a test can assert what the guest would actually receive.
	// Nothing reports it back, so it never leaves the fake.
	userData string
}

type fakeIface struct {
	PortID     string
	NetworkID  string
	MacAddress string
	PrimaryIP  string
	IsPublic   bool
	PortSec    bool
	SecGroups  []string
}

type fakeVol struct {
	ID   string
	Name string
	Size int

	// status is what the volume's own details endpoint reports: `in-use` while
	// attached, `detaching` once a detach is accepted, then whatever detachEndsAs
	// says. settleReads counts the details reads it stays in `detaching` — the
	// real platform takes minutes over this, and a wait that settles in one read
	// proves nothing.
	status        string
	settleReads   int
	detachEndsAs  string
	detachedFromA string
}

// fakeVMAPI stands in for the VM routes.
type fakeVMAPI struct {
	mu  sync.Mutex
	vms map[string]*fakeVM

	// buildPolls is how many BUILD responses a new VM returns before ACTIVE.
	buildPolls int
	// failWithError makes newly created VMs settle into ERROR instead.
	failWithError bool
	// detachRevertsLeft is how many detaches fail the way a busy guest makes them
	// fail: accepted, then undone, with the volume back in `in-use`. Counting
	// rather than latching leaves the test a way out — the operator unmounts the
	// filesystem and the next detach works, which is also what the framework's
	// own cleanup needs.
	detachRevertsLeft int

	createdNames []string
	renames      int
	resizes      int
	powerOps     []string
	hotPlugOps   int
	nextPort     int
}

func newFakeVMAPI() *fakeVMAPI {
	return &fakeVMAPI{vms: map[string]*fakeVM{}, buildPolls: 1}
}

func (f *fakeVMAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("x-api-access-key") == "" || r.Header.Get("x-api-secret-key") == "" {
		acctest.WriteJSON(w, http.StatusUnauthorized, map[string]any{"errorMessage": "missing api key headers"})
		return
	}
	if r.URL.Query().Get("serverId") == "" {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "serverId is required"})
		return
	}

	// The provider resolves flavor names to ids, so the flavors endpoint is
	// answered here too.
	if r.Method == http.MethodGet && r.URL.Path == "/openstack/flavors" {
		catalogue := make([]map[string]any, 0, len(fakeFlavors))
		for _, name := range []string{"flavor-small", "flavor-large"} {
			f := fakeFlavors[name]
			catalogue = append(catalogue, map[string]any{
				"id": name, "name": name, "vcpus": f.vcpus, "ram": f.ram,
			})
		}
		acctest.WriteJSON(w, http.StatusOK, catalogue)
		return
	}

	// The volume's own details endpoint. The detach wait reads this rather than
	// the VM's volume list, because the list cannot tell a finished detach from
	// a refused one.
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/openstack/volumes/") &&
		strings.HasSuffix(r.URL.Path, "/details") {
		f.volumeDetails(w, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/openstack/volumes/"), "/details"))
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/openstack/vms")

	seg := strings.Split(strings.Trim(path, "/"), "/")
	id := ""
	if len(seg) > 0 {
		id = seg[0]
	}

	switch {
	case r.Method == http.MethodPost && path == "":
		f.create(w, r)
	case r.Method == http.MethodGet && path == "":
		f.list(w)
	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "details":
		f.details(w, id)
	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "networks":
		f.listIfaces(w, id)
	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "history":
		f.history(w, id)
	case r.Method == http.MethodGet && len(seg) == 4 && seg[1] == "history" && seg[3] == "detail":
		f.historyDetail(w, id, seg[2])
	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "volumes":
		f.listVolumes(w, id)
	case r.Method == http.MethodPost && len(seg) == 2 && seg[1] == "actions":
		f.action(w, r, id)
	case r.Method == http.MethodPost && len(seg) == 2 && seg[1] == "volume":
		f.attachVolume(w, r, id)
	case r.Method == http.MethodPost && len(seg) == 2 && seg[1] == "networks":
		f.attachIface(w, r, id)
	case r.Method == http.MethodPut && len(seg) == 2 && seg[1] == "hot-plug":
		f.hotPlug(w, r, id)
	case r.Method == http.MethodDelete && len(seg) == 3 && seg[1] == "volumes":
		f.detachVolume(w, id, seg[2])
	case r.Method == http.MethodPut && len(seg) == 3 && seg[1] == "network":
		f.updateIface(w, r, id, seg[2])
	case r.Method == http.MethodDelete && len(seg) == 3 && seg[1] == "network":
		f.detachIface(w, id, seg[2])
	case r.Method == http.MethodPut && len(seg) == 1:
		f.rename(w, r, id)
	case r.Method == http.MethodDelete && len(seg) == 1:
		f.delete(w, id)
	default:
		acctest.WriteJSON(w, http.StatusNotFound, map[string]any{"errorMessage": "no such route"})
	}
}

func (f *fakeVMAPI) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		FlavorRef string `json:"flavorRef"`
		Networks  []struct {
			UUID string `json:"uuid"`
			// Pointers so a missing field and an explicit null are distinguishable:
			// null is rejected, and the fake has to reject it too.
			SecurityGroups      *[]string      `json:"security_groups"`
			FixedIps            *[]interface{} `json:"fixed_ips"`
			PortSecurityEnabled bool           `json:"port_security_enabled"`
		} `json:"networks"`
		BlockDeviceMapping []struct {
			BootIndex int `json:"boot_index"`
		} `json:"block_device_mapping_v2"`
		EnableHotPlug *bool  `json:"enableHotPlug"`
		UserData      string `json:"user_data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "bad body"})
		return
	}
	// Mirror the required fields so a malformed request fails here rather than
	// silently passing the test.
	if body.Name == "" || body.FlavorRef == "" || len(body.Networks) == 0 || len(body.BlockDeviceMapping) == 0 {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "name, flavorRef, networks and block_device_mapping_v2 are required"})
		return
	}
	// These must be arrays; null is rejected. Reproducing that stops a nil slice
	// reaching production.
	for i, n := range body.Networks {
		if n.SecurityGroups == nil {
			acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{
				"errorMessage": fmt.Sprintf("'networks[%d].security_groups' must be an array", i)})
			return
		}
		if n.FixedIps == nil {
			acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{
				"errorMessage": fmt.Sprintf("'networks[%d].fixed_ips' must be an array", i)})
			return
		}
		// An empty fixed_ips builds a port with no address, leaving the VM
		// unreachable. Rejected so the provider cannot regress to sending one.
		if len(*n.FixedIps) == 0 {
			acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{
				"errorMessage": fmt.Sprintf("'networks[%d].fixed_ips' is empty; the VM would get no IP address", i)})
			return
		}
	}

	// user_data has to arrive base64-encoded; a raw cloud-config document is
	// refused. Reproducing that keeps the encoding on the provider's side of the
	// line, where the API put it.
	userData, err := base64.StdEncoding.DecodeString(body.UserData)
	if err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"errorMessage": fmt.Sprintf("Invalid input for field/attribute user_data. %q is not a 'base64'", body.UserData)})
		return
	}

	f.mu.Lock()
	id := fmt.Sprintf("%s-%d", fakeVMID, len(f.vms))
	f.nextPort++
	f.vms[id] = &fakeVM{
		ID:        id,
		Name:      body.Name,
		Flavor:    body.FlavorRef,
		pollsLeft: f.buildPolls,
		// The boot interface keeps what create asked for. The interface list
		// reports the groups and port security back, and an import relies on it.
		ifaces: []*fakeIface{{
			PortID:    fmt.Sprintf("port-boot-%d", f.nextPort),
			NetworkID: body.Networks[0].UUID,
			PrimaryIP: "10.0.0.10",
			IsPublic:  true,
			PortSec:   body.Networks[0].PortSecurityEnabled,
			SecGroups: derefStrings(body.Networks[0].SecurityGroups),
		}},
		volumes:  []*fakeVol{{ID: "vol-boot", Name: body.Name + "-boot", Size: 20}},
		hotPlug:  body.EnableHotPlug != nil && *body.EnableHotPlug,
		userData: string(userData),
	}
	f.createdNames = append(f.createdNames, body.Name)
	f.mu.Unlock()

	// The server object comes back unwrapped: the id is top level.
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"id": id})
}

// statusNow is the VM's settled status, ignoring any in-flight transition.
func (v *fakeVM) statusNow() string {
	switch {
	case v.shelved:
		return "SHELVED_OFFLOADED"
	case v.stopped:
		return "SHUTOFF"
	}
	return "ACTIVE"
}

// prevStatus is what the VM reports while a power transition is still running.
func (v *fakeVM) prevStatus() string {
	if v.before != "" {
		return v.before
	}
	return "ACTIVE"
}

// fakeFlavors is the size catalogue the fake serves and reports against. An
// unknown flavor reads as the zero value.
var fakeFlavors = map[string]struct {
	vcpus int
	ram   string
}{
	"flavor-small": {vcpus: 1, ram: "512 MB"},
	"flavor-large": {vcpus: 4, ram: "8192 MB"},
}

// fakeFixedIP mirrors the fixed_ips entries the API accepts.
type fakeFixedIP struct {
	IPVersion int    `json:"ip_version"`
	IPAddress string `json:"ip_address"`
}

// pinnedOr returns the first pinned address, or fallback when none is pinned.
func pinnedOr(ips []fakeFixedIP, fallback string) string {
	for _, ip := range ips {
		if ip.IPAddress != "" {
			return ip.IPAddress
		}
	}
	return fallback
}

func (f *fakeVMAPI) detailsBody(id, status, taskState string) map[string]any {
	f.vmsMu(id)
	v := f.vms[id]
	return map[string]any{
		"id":           id,
		"name":         v.Name,
		"status":       status,
		"creationTime": acctest.FakeCreatedAt,
		"lastModified": acctest.FakeCreatedAt,
		"image":        fakeVMImage,
		// The id alongside the name: image names are not unique, so the id is
		// what lets a read reconcile a boot disk against its source.
		"imageId":     fakeVMImageID,
		"imageOsType": fakeVMOsType,
		"sshKey":      fakeVMKeyName,
		"taskState":   taskState,
		// From the same catalogue the flavors endpoint serves, so a resized
		// instance reports the size it was resized to. A fixed answer here hid
		// that vcpus and ram move with the flavor.
		"flavor": map[string]any{
			"name":  v.Flavor,
			"vcpus": fakeFlavors[v.Flavor].vcpus,
			"ram":   fakeFlavors[v.Flavor].ram,
		},
		// dt-go's typed struct does not carry these two, so the provider reads
		// them out of the raw body. Reporting them makes that path testable.
		"hotPlugEnabled": v.hotPlug,
		"metadata":       map[string]string{"ha_enabled": "true"},
	}
}

// vmsMu is a no-op marker: detailsBody is always called with f.mu held.
func (f *fakeVMAPI) vmsMu(string) {}

func (f *fakeVMAPI) details(w http.ResponseWriter, id string) {
	f.mu.Lock()
	v, ok := f.vms[id]
	if !ok || v.deleted {
		f.mu.Unlock()
		acctest.NotFound(w, "virtual machine not found")
		return
	}

	var body map[string]any
	switch {
	case v.pollsLeft > 0:
		v.pollsLeft--
		body = f.detailsBody(id, "BUILD", "spawning")
	case f.failWithError:
		body = f.detailsBody(id, "ERROR", "")
		// `fault` is the only thing that says why a build failed, so reporting it
		// pins the provider surfacing it instead of a bare "entered ERROR state".
		body["fault"] = fakeVMFault
	case v.settlePolls > 0:
		v.settlePolls--
		body = f.detailsBody(id, v.prevStatus(), "powering")
	case v.verifyPolls > 0:
		v.verifyPolls--
		if v.verifyPolls == 0 && v.pendingFlavor != "" {
			v.Flavor, v.pendingFlavor = v.pendingFlavor, ""
		}
		// First poll still shows the old flavor on the old status.
		body = f.detailsBody(id, v.statusNow(), "resizing")
	default:
		body = f.detailsBody(id, v.statusNow(), "")
	}
	f.mu.Unlock()

	acctest.WriteJSON(w, http.StatusOK, body)
}

func (f *fakeVMAPI) rename(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	v, ok := f.vms[id]
	if !ok || v.deleted {
		f.mu.Unlock()
		acctest.WriteJSON(w, http.StatusNotFound, map[string]any{"errorMessage": "virtual machine not found"})
		return
	}
	v.Name = body.Name
	f.renames++
	f.mu.Unlock()

	acctest.WriteJSON(w, http.StatusOK, map[string]any{"server": map[string]any{"id": id}})
}

func (f *fakeVMAPI) delete(w http.ResponseWriter, id string) {
	f.mu.Lock()
	v, ok := f.vms[id]
	if ok {
		v.deleted = true
	}
	f.mu.Unlock()

	if !ok {
		acctest.WriteJSON(w, http.StatusNotFound, map[string]any{"errorMessage": "virtual machine not found"})
		return
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}

func (f *fakeVMAPI) list(w http.ResponseWriter) {
	f.mu.Lock()
	out := make([]map[string]any, 0, len(f.vms))
	for _, v := range f.vms {
		if v.deleted {
			continue
		}
		status := "ACTIVE"
		if v.stopped {
			status = "SHUTOFF"
		}
		out = append(out, map[string]any{
			"id": v.ID, "name": v.Name, "status": status,
			"ipAddress": []string{"10.0.0.10"}, "vcpus": 1, "ram": "512 MB",
			"storage": 20, "volumeSize": 20, "taskState": "",
		})
	}
	f.mu.Unlock()
	// Keep the order stable so the data source's hashed id does not flap.
	sort.Slice(out, func(i, j int) bool { return out[i]["id"].(string) < out[j]["id"].(string) })
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeVMAPI) withVM(w http.ResponseWriter, id string, fn func(*fakeVM)) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.vms[id]
	if !ok || v.deleted {
		acctest.WriteJSON(w, http.StatusNotFound, map[string]any{"errorMessage": "virtual machine not found"})
		return false
	}
	fn(v)
	return true
}

// history is an event log. The list leaves `status` out — only the detail
// endpoint reports it, which is why there are two data sources.
func (f *fakeVMAPI) history(w http.ResponseWriter, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.vms[id]; !ok {
		acctest.NotFound(w, "vm not found")
		return
	}
	acctest.WriteJSON(w, http.StatusOK, []map[string]any{
		{"id": "hist-2", "dateAndTime": acctest.FakeCreatedAt, "activity": "Start", "initiator": "test-user"},
		{"id": "hist-1", "dateAndTime": acctest.FakeCreatedAt, "activity": "Create", "initiator": "test-user"},
	})
}

func (f *fakeVMAPI) historyDetail(w http.ResponseWriter, id, historyID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.vms[id]; !ok {
		acctest.NotFound(w, "vm not found")
		return
	}
	if historyID != "hist-1" && historyID != "hist-2" {
		acctest.NotFound(w, "history entry not found")
		return
	}
	activity := "Create"
	if historyID == "hist-2" {
		activity = "Start"
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{
		"id": historyID, "dateAndTime": acctest.FakeCreatedAt,
		"activity": activity, "initiator": "test-user", "status": "Success",
	})
}

func (f *fakeVMAPI) listIfaces(w http.ResponseWriter, id string) {
	var out []map[string]any
	if !f.withVM(w, id, func(v *fakeVM) {
		for _, i := range v.ifaces {
			groups := make([]map[string]any, 0, len(i.SecGroups))
			for _, g := range i.SecGroups {
				groups = append(groups, map[string]any{"id": g, "name": g + "-name"})
			}
			out = append(out, map[string]any{
				"portId": i.PortID, "id": i.NetworkID, "networkName": "net-" + i.NetworkID,
				"macAddress": i.MacAddress, "primaryIp": i.PrimaryIP, "secondaryIps": []string{},
				"isPublic": i.IsPublic, "spoofingProtection": i.PortSec, "securityGroups": groups,
			})
		}
	}) {
		return
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeVMAPI) listVolumes(w http.ResponseWriter, id string) {
	var out []map[string]any
	if !f.withVM(w, id, func(v *fakeVM) {
		for _, vol := range v.volumes {
			if vol.status == "available" {
				continue
			}
			out = append(out, map[string]any{
				"id": vol.ID, "name": vol.Name, "storagePolicy": "standard",
				"size": vol.Size, "deleteOnTermination": vol.ID == "vol-boot",
			})
		}
	}) {
		return
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}

// volumeDetails reports one volume's status, advancing a pending detach by one
// read. The countdown is what makes a broken wait visible: a wait that stops
// before the volume settles reads `detaching` and is wrong about it.
func (f *fakeVMAPI) volumeDetails(w http.ResponseWriter, volumeID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, v := range f.vms {
		for _, vol := range v.volumes {
			if vol.ID != volumeID {
				continue
			}
			if vol.status == "detaching" {
				if vol.settleReads > 0 {
					vol.settleReads--
				} else {
					vol.status = vol.detachEndsAs
				}
			}
			acctest.WriteJSON(w, http.StatusOK, map[string]any{
				"id": vol.ID, "name": vol.Name, "status": vol.status, "size": vol.Size,
				"attachedTo": vol.detachedFromA, "attachedToId": v.ID, "type": "standard",
				"isDetachable": true,
			})
			return
		}
	}
	acctest.NotFound(w, fmt.Sprintf("volume %s not found", volumeID))
}

func (f *fakeVMAPI) action(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Action    string `json:"action"`
		FlavorRef string `json:"flavorRef"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	resizeErr := false
	if !f.withVM(w, id, func(v *fakeVM) {
		switch body.Action {
		case "start":
			v.before = v.statusNow()
			v.stopped, v.shelved, v.settlePolls = false, false, 2
		case "softStop", "hardStop":
			v.before = v.statusNow()
			v.stopped, v.settlePolls = true, 2
		case "shelve":
			v.before = v.statusNow()
			v.shelved, v.settlePolls = true, 2
		case "unshelve":
			v.before = v.statusNow()
			v.shelved, v.stopped, v.settlePolls = false, false, 2
		case "resize":
			// The platform refuses to resize a running instance.
			if !v.stopped {
				resizeErr = true
				return
			}
			// The new flavor is not visible immediately: the VM stays SHUTOFF on the
			// old flavor, then goes through VERIFY_RESIZE. This catches a completion
			// check that only looks at the status.
			v.pendingFlavor = body.FlavorRef
			v.verifyPolls = 3
			f.resizes++
		}
		if body.Action != "resize" {
			f.powerOps = append(f.powerOps, body.Action)
		}
	}) {
		return
	}
	if resizeErr {
		acctest.WriteJSON(w, http.StatusConflict, map[string]any{
			"errorMessage": "instance must be stopped before it can be resized"})
		return
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "ok"})
}

func (f *fakeVMAPI) hotPlug(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !f.withVM(w, id, func(v *fakeVM) { v.hotPlug = body.Enabled; f.hotPlugOps++ }) {
		return
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "ok"})
}

func (f *fakeVMAPI) attachVolume(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		VolumeID string `json:"volumeId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.VolumeID == "" {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "volumeId is required"})
		return
	}
	if !f.withVM(w, id, func(v *fakeVM) {
		v.volumes = append(v.volumes, &fakeVol{
			ID: body.VolumeID, Name: body.VolumeID + "-name", Size: 50, status: "in-use", detachedFromA: v.Name,
		})
	}) {
		return
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "attached"})
}

// detachVolume accepts the request and leaves the work unfinished, which is what
// the platform does: the reply is 202 and the guest decides the rest. The volume
// stays on the VM's list throughout — on a refusal it is still there afterwards,
// so the list never distinguishes the two outcomes.
func (f *fakeVMAPI) detachVolume(w http.ResponseWriter, id, volumeID string) {
	if !f.withVM(w, id, func(v *fakeVM) {
		for _, vol := range v.volumes {
			if vol.ID != volumeID {
				continue
			}
			vol.status = "detaching"
			vol.settleReads = 4
			vol.detachEndsAs = "available"
			if f.detachRevertsLeft > 0 {
				// A guest that will not release the filesystem: the platform
				// gives up and puts the volume back.
				f.detachRevertsLeft--
				vol.detachEndsAs = "in-use"
			}
		}
	}) {
		return
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "detached"})
}

func (f *fakeVMAPI) attachIface(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		NetworkID      string         `json:"network_id"`
		MacAddress     string         `json:"mac_address"`
		SecurityGroups []string       `json:"security_groups"`
		PortSec        bool           `json:"port_security_enabled"`
		FixedIps       *[]fakeFixedIP `json:"fixed_ips"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.NetworkID == "" {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "network_id is required"})
		return
	}
	// The attach endpoint requires fixed_ips, and an empty list builds a port
	// with no address, as on the create path.
	if body.FixedIps == nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "'fixed_ips' must be an array"})
		return
	}
	if len(*body.FixedIps) == 0 {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "'fixed_ips' is empty; the interface would get no IP address"})
		return
	}
	if !f.withVM(w, id, func(v *fakeVM) {
		f.nextPort++
		v.ifaces = append(v.ifaces, &fakeIface{
			PortID:    fmt.Sprintf("port-extra-%d", f.nextPort),
			NetworkID: body.NetworkID,
			// The platform assigns these, so the fake does too.
			MacAddress: "fa:16:3e:00:00:0" + fmt.Sprint(f.nextPort),
			// A pinned address is honoured; otherwise the platform allocates.
			PrimaryIP: pinnedOr(*body.FixedIps, fmt.Sprintf("10.0.1.%d", f.nextPort)),
			PortSec:   body.PortSec,
			SecGroups: body.SecurityGroups,
		})
	}) {
		return
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "attached"})
}

func (f *fakeVMAPI) updateIface(w http.ResponseWriter, r *http.Request, id, portID string) {
	var body struct {
		SecurityGroups []string       `json:"security_groups"`
		FixedIps       *[]fakeFixedIP `json:"fixed_ips"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	// The update endpoint requires fixed_ips and rejects an empty entry.
	if body.FixedIps == nil || len(*body.FixedIps) == 0 {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "'fixed_ips' must be a non-empty array"})
		return
	}
	for _, ip := range *body.FixedIps {
		if ip.IPVersion == 0 && ip.IPAddress == "" {
			acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "'fixed_ips[0]' must contain at least one of [ip_version, ip_address]"})
			return
		}
	}
	if !f.withVM(w, id, func(v *fakeVM) {
		for _, i := range v.ifaces {
			if i.PortID == portID {
				i.SecGroups = body.SecurityGroups
				i.PrimaryIP = pinnedOr(*body.FixedIps, i.PrimaryIP)
			}
		}
	}) {
		return
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "updated"})
}

func (f *fakeVMAPI) detachIface(w http.ResponseWriter, id, portID string) {
	if !f.withVM(w, id, func(v *fakeVM) {
		kept := v.ifaces[:0]
		for _, i := range v.ifaces {
			if i.PortID != portID {
				kept = append(kept, i)
			}
		}
		v.ifaces = kept
	}) {
		return
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "detached"})
}

func testVMConfig(endpoint, name, flavor string) string {
	return testVMConfigState(endpoint, name, flavor, "running")
}

// vmOutputs exposes the flavor-derived attributes through outputs. State comes
// back from Read and is always fresh; an output is evaluated from the plan, so
// an attribute the plan believed unchanged reads stale there for a whole cycle.
const vmOutputs = `
output "vm_vcpus" {
  value = dtcloud_vm.test.vcpus
}

output "vm_ram" {
  value = dtcloud_vm.test.ram
}
`

func testVMConfigState(endpoint, name, flavor, state string) string {
	return fmt.Sprintf(`
provider "dtcloud" {
  access_key   = "test-access-key"
  secret_key   = "test-secret-key"
  api_endpoint = %q
  region_id    = "1"
}

resource "dtcloud_vm" "test" {
  name      = %q
  flavor_id = %q
  state     = %q
  key_name  = %q

  network {
    uuid            = "11111111-2222-3333-4444-555555555555"
    security_groups = ["sg-default"]
  }

  block_device {
    boot_index            = 0
    volume_size           = 20
    source_type           = "image"
    device_type           = "disk"
    destination_type      = "volume"
    delete_on_termination = true
    volume_type           = "standard"
    uuid                  = "img-0001"
  }
}

data "dtcloud_vm" "test" {
  id = dtcloud_vm.test.id
}

data "dtcloud_vm_history" "test" {
  vm_id = dtcloud_vm.test.id
}

data "dtcloud_vm_history_entry" "first" {
  vm_id      = dtcloud_vm.test.id
  history_id = "hist-1"
}
`, endpoint, name, flavor, state, fakeVMKeyName)
}

// TestAccDtcloudVM_lifecycle drives create (including the wait for ACTIVE) →
// read → re-plan → in-place rename → ForceNew on flavor → import → destroy.

func testVMAttachmentConfig(endpoint, extra string) string {
	return fmt.Sprintf(`
provider "dtcloud" {
  access_key   = "test-access-key"
  secret_key   = "test-secret-key"
  api_endpoint = %q
  region_id    = "1"
}

resource "dtcloud_vm" "host" {
  name      = "tf-acc-host"
  flavor_id = "flavor-small"
  key_name  = "tf-acc-key"

  network {
    uuid            = "11111111-2222-3333-4444-555555555555"
    security_groups = ["sg-default"]
  }

  block_device {
    boot_index            = 0
    volume_size           = 20
    source_type           = "image"
    device_type           = "disk"
    destination_type      = "volume"
    delete_on_termination = true
    volume_type           = "standard"
    uuid                  = "img-0001"
  }
}
%s
`, endpoint, extra)
}

// TestAccDtcloudVMVolumeAttachment covers attach → read → import → detach, and
// checks the attachment shows up on the VM's own volume list.

// derefStrings unwraps the pointer-to-slice the create body uses to tell
// "field absent" from "empty array".
func derefStrings(v *[]string) []string {
	if v == nil {
		return nil
	}
	return *v
}
