package volume_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
)

// noHTML is the Joi rule from createVolumeSchema / updateVolumeSchema.
var noHTML = regexp.MustCompile(`^[^<>&"']*$`)

// settleReads is how many details reads a status change takes to land, so that
// the create and delete waiters are exercised rather than satisfied on the
// first poll.
const settleReads = 2

// valueSettleReads is the same idea for extend and retype, and it is
// deliberately larger. Those two are the traps: the volume sits at its resting
// status reporting its old size or policy, so a waiter watching only the status
// returns early.
//
// This delay is synthetic and larger than anything observed. A 1 GB extend on
// DEV landed before the first poll, about a second after the action was
// accepted — so a live run would not have caught a status-only waiter at that
// size. The fake exaggerates on purpose: it models the case the waiter has to
// survive, not the one that happens to be fast today.
//
// The number has to keep the stale value visible for longer than a broken
// provider takes to stop looking. A status-only wait costs two reads —
// ContinuousTargetOccurence is two — and the Read that follows the update costs
// a third, so the old value has to survive at least three. Two did not: a
// deliberately reverted size wait still passed, because the value had drained
// away by the time Read looked. Four holds it one read past the threshold.
const valueSettleReads = 4

type fakeSnapshot struct {
	ID            string
	Name          string
	Status        string
	Description   string
	Size          int
	StoragePolicy string
}

type fakeVolume struct {
	ID            string
	Name          string
	Status        string
	StoragePolicy string
	Size          int
	Bootable      string
	Type          string
	// Held but never reported: getVolumeDetailsForClient builds its response
	// field by field and leaves the description out entirely.
	Description string
	ImageID     string
	ImageName   string
	DiskFormat  string

	AttachedTo       string
	AttachedToID     string
	AttachedToStatus string

	// Pending changes, each with a countdown in details reads. Applying them
	// lazily is what makes the fake lie the way the platform lies.
	pendingStatus string
	statusDelay   int
	pendingSize   int
	sizeDelay     int
	pendingPolicy string
	policyDelay   int

	deleted     bool
	deleteDelay int

	snapshots []fakeSnapshot
}

// tick advances every pending change by one details read. The caller reports
// the volume *before* calling this, so a change with a delay of n is invisible
// for n reads and only then becomes visible — never on the read that triggered
// it.
//
// All the counters advance together rather than one at a time. Draining them in
// turn would mean a wait for one change also settled another, which is not how
// the platform behaves and which hides exactly the bug this fake is built to
// catch: a wait that watches the wrong thing gets covered by the next wait that
// watches the right one.
func (v *fakeVolume) tick() {
	if v.deleteDelay > 0 {
		v.deleteDelay--
		if v.deleteDelay == 0 {
			v.deleted = true
		}
	}
	if v.statusDelay > 0 {
		v.statusDelay--
		if v.statusDelay == 0 {
			v.Status = v.pendingStatus
		}
	}
	if v.sizeDelay > 0 {
		v.sizeDelay--
		if v.sizeDelay == 0 {
			v.Size = v.pendingSize
		}
	}
	if v.policyDelay > 0 {
		v.policyDelay--
		if v.policyDelay == 0 {
			v.StoragePolicy = v.pendingPolicy
		}
	}
}

// imageMeta mirrors the OpenStack sub-object, present only on image-backed
// volumes. Kept a pointer so it can be omitted rather than sent empty.
type imageMeta struct {
	OsDistro         string `json:"os_distro"`
	ImageValidated   string `json:"image_validated"`
	HwQemuGuestAgent string `json:"hw_qemu_guest_agent"`
	OsType           string `json:"os_type"`
	ImageID          string `json:"image_id"`
	ImageName        string `json:"image_name"`
	Checksum         string `json:"checksum"`
	ContainerFormat  string `json:"container_format"`
	DiskFormat       string `json:"disk_format"`
	MinDisk          string `json:"min_disk"`
	MinRAM           string `json:"min_ram"`
	Size             string `json:"size"`
}

// detailsResponse is a struct rather than a map on purpose.
//
// Go sorts map keys alphabetically when encoding, which would move `created`
// to the front and hide the class of bug where a field that fails to decode
// takes everything declared after it down with it. This order is the order
// getVolumeDetailsForClient builds its object in.
type detailsResponse struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	Status              string     `json:"status"`
	AttachedTo          string     `json:"attachedTo"`
	AttachedToStatus    string     `json:"attachedToStatus"`
	AttachedToID        string     `json:"attachedToId"`
	Size                int        `json:"size"`
	StoragePolicy       string     `json:"storagePolicy"`
	Bootable            string     `json:"bootable"`
	Created             string     `json:"created"`
	LastModified        string     `json:"lastModified"`
	VolumeImageMetadata *imageMeta `json:"volume_image_metadata,omitempty"`
	Type                string     `json:"type"`
	IsDetachable        bool       `json:"isDetachable"`
}

// fakeVolumeAPI stands in for cloud-web-api's /openstack/volumes routes.
//
// The rules it enforces are the real API's, not the ones this provider finds
// convenient. In particular it reproduces:
//
//   - create answering with `volumeId` while clone answers with `id`;
//   - the list endpoint sending size as the string "20 GB";
//   - `bootable` as a string rather than a boolean;
//   - details never carrying a description, however many times one is written;
//   - extend and retype leaving the volume at `available` with its old value
//     for several reads;
//   - `.or(...)` on the actions body, and the create schema's bounds.
type fakeVolumeAPI struct {
	mu      sync.Mutex
	volumes map[string]*fakeVolume
	seq     int

	creates  int
	clones   int
	extends  int
	retypes  int
	updates  int
	detailsN int

	policies []map[string]string
}

func newFakeVolumeAPI() *fakeVolumeAPI {
	return &fakeVolumeAPI{
		volumes: map[string]*fakeVolume{},
		policies: []map[string]string{
			{"id": "type-0001", "name": "standard"},
			{"id": "type-0002", "name": "fast"},
		},
	}
}

func (f *fakeVolumeAPI) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%04d", prefix, f.seq)
}

func (f *fakeVolumeAPI) validPolicy(name string) bool {
	for _, p := range f.policies {
		if p["name"] == name {
			return true
		}
	}
	return false
}

func (f *fakeVolumeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !acctest.RequireAuth(w, r) {
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/openstack/volumes")
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
	// Declared before the {id} routes, as it is in the router: a literal
	// segment must not be read as a volume id.
	case r.Method == http.MethodGet && len(seg) == 1 && seg[0] == "storage-policies":
		acctest.WriteJSON(w, http.StatusOK, f.policies)
	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "details":
		f.details(w, seg[0])
	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "snapshots":
		f.snapshots(w, seg[0])
	case r.Method == http.MethodPost && len(seg) == 2 && seg[1] == "clone":
		f.clone(w, r, seg[0])
	case r.Method == http.MethodPost && len(seg) == 2 && seg[1] == "actions":
		f.actions(w, r, seg[0])
	case r.Method == http.MethodPost && len(seg) == 1:
		f.update(w, r, seg[0])
	case r.Method == http.MethodDelete && len(seg) == 1:
		f.delete(w, seg[0])
	default:
		acctest.NotFound(w, "no such route")
	}
}

func (f *fakeVolumeAPI) get(w http.ResponseWriter, id string) *fakeVolume {
	v, ok := f.volumes[id]
	if !ok || v.deleted {
		acctest.NotFound(w, "volume not found")
		return nil
	}
	return v
}

// validationError is the 406 the Joi middleware produces.
func validationError(w http.ResponseWriter, message string) {
	acctest.WriteJSON(w, http.StatusNotAcceptable, map[string]any{"errorMessage": message})
}

func (f *fakeVolumeAPI) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name          string `json:"name"`
		StoragePolicy string `json:"storagePolicy"`
		Size          int    `json:"size"`
		ImageRef      string `json:"imageRef"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "malformed body"})
		return
	}

	// createVolumeSchema, in the order it declares things.
	if body.Name == "" {
		validationError(w, "'name' is required")
		return
	}
	if !noHTML.MatchString(body.Name) {
		validationError(w, "Invalid characters in 'name'. HTML tags and special characters (<, >, &, ', \") are not allowed.")
		return
	}
	if body.Size < 1 || body.Size > 8192 {
		validationError(w, "'size' must be between 1 and 8192")
		return
	}
	if body.StoragePolicy == "" {
		validationError(w, "'storagePolicy' is required")
		return
	}
	if !f.validPolicy(body.StoragePolicy) {
		acctest.WriteJSON(w, http.StatusInternalServerError,
			map[string]any{"errorMessage": fmt.Sprintf("volume type %q not found", body.StoragePolicy)})
		return
	}

	v := &fakeVolume{
		ID:            f.nextID("vol"),
		Name:          body.Name,
		StoragePolicy: body.StoragePolicy,
		Size:          body.Size,
		Type:          "HDD",
		Bootable:      "false",
		// Fresh volumes are `creating` for a while. Every non-target status has
		// to be treated as pending by the provider, which is the rule that
		// enumerating transitional states broke on VMs.
		Status:        "creating",
		pendingStatus: "available",
		statusDelay:   settleReads,
	}
	if body.ImageRef != "" {
		v.Bootable = "true"
		v.ImageID = body.ImageRef
		v.ImageName = "image-" + body.ImageRef
		v.DiskFormat = "qcow2"
		// An image-backed volume passes through `downloading` too — another
		// status nobody would think to enumerate.
		v.Status = "downloading"
	}
	f.volumes[v.ID] = v
	f.creates++

	// createVolumeForClient builds this object itself, and keys the id
	// `volumeId`. Nothing here is called `id`.
	acctest.WriteJSON(w, http.StatusOK, map[string]any{
		"volumeId":         v.ID,
		"name":             v.Name,
		"status":           v.Status,
		"policy":           v.StoragePolicy,
		"size":             v.Size,
		"bootable":         v.Bootable,
		"type":             v.Type,
		"diskSerialNumber": v.ID,
	})
}

func (f *fakeVolumeAPI) clone(w http.ResponseWriter, r *http.Request, sourceID string) {
	source := f.get(w, sourceID)
	if source == nil {
		return
	}

	var body struct {
		Name          string `json:"name"`
		StoragePolicy string `json:"storagePolicy"`
		Size          int    `json:"size"`
		ImageRef      string `json:"imageRef"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "malformed body"})
		return
	}
	// cloneVolumeSchema: name, size and storagePolicy, all required. imageRef
	// is not a member — Joi rejects unknown keys.
	if body.Name == "" || body.StoragePolicy == "" || body.Size == 0 {
		validationError(w, "'name', 'size' and 'storagePolicy' are required")
		return
	}
	if body.ImageRef != "" {
		validationError(w, "'imageRef' is not allowed")
		return
	}

	v := &fakeVolume{
		ID:            f.nextID("vol"),
		Name:          body.Name,
		StoragePolicy: body.StoragePolicy,
		Size:          body.Size,
		Type:          source.Type,
		Bootable:      source.Bootable,
		ImageID:       source.ImageID,
		ImageName:     source.ImageName,
		DiskFormat:    source.DiskFormat,
		Status:        "creating",
		pendingStatus: "available",
		statusDelay:   settleReads,
	}
	f.volumes[v.ID] = v
	f.clones++

	// The clone route returns whatever createVolume returned, which is the raw
	// OpenStack volume — so `id`, `volume_type`, `created_at`. Nothing in this
	// body is called `volumeId`, and the provider has to cope with both shapes.
	acctest.WriteJSON(w, http.StatusOK, map[string]any{
		"id":          v.ID,
		"status":      v.Status,
		"size":        v.Size,
		"created_at":  acctest.FakeCreatedAt,
		"updated_at":  acctest.FakeCreatedAt,
		"name":        v.Name,
		"description": "",
		"volume_type": v.StoragePolicy,
	})
}

func (f *fakeVolumeAPI) details(w http.ResponseWriter, id string) {
	v := f.get(w, id)
	if v == nil {
		return
	}
	f.detailsN++

	out := detailsResponse{
		ID:               v.ID,
		Name:             v.Name,
		Status:           v.Status,
		AttachedTo:       v.AttachedTo,
		AttachedToStatus: v.AttachedToStatus,
		AttachedToID:     v.AttachedToID,
		Size:             v.Size,
		StoragePolicy:    v.StoragePolicy,
		// A string, as OpenStack sends it — not a JSON boolean.
		Bootable:     v.Bootable,
		Created:      acctest.FakeCreatedAt,
		LastModified: acctest.FakeCreatedAt,
		Type:         v.Type,
		// false while detached, and it disagrees with the list route on the
		// very same volume — see the note on list(). Confirmed live.
		IsDetachable: v.AttachedToID != "",
	}
	if v.ImageID != "" {
		out.VolumeImageMetadata = &imageMeta{
			OsDistro:        "ubuntu",
			OsType:          "linux",
			ImageID:         v.ImageID,
			ImageName:       v.ImageName,
			Checksum:        "d41d8cd98f00b204e9800998ecf8427e",
			ContainerFormat: "bare",
			DiskFormat:      v.DiskFormat,
			MinDisk:         "5",
			MinRAM:          "512",
			Size:            "1073741824",
		}
	}

	// Reported first, advanced after: a pending change is never visible on the
	// read that would have been able to see it too early.
	acctest.WriteJSON(w, http.StatusOK, out)
	v.tick()
}

func (f *fakeVolumeAPI) list(w http.ResponseWriter) {
	out := []map[string]any{}
	for _, v := range f.volumes {
		if v.deleted {
			continue
		}
		out = append(out, map[string]any{
			"id":     v.ID,
			"name":   v.Name,
			"status": v.Status,
			"policy": v.StoragePolicy,
			// The list route builds this as `volume.size + ' GB'`, so it is a
			// string here and a number on the details route.
			"size":         fmt.Sprintf("%d GB", v.Size),
			"bootable":     v.Bootable,
			"attachedTo":   v.AttachedTo,
			"attachedToId": v.AttachedToID,
			"vmStatus":     v.AttachedToStatus,
			"type":         v.Type,
			// Always true for a detached volume, where the details route says
			// false for the same one. Not a bug in this fake: the two handlers
			// compute it differently. getVolumeDetailsForClient asks
			// getAttachedVmInfoHelper, which returns false outright when there
			// is no server; getVolumesForClient writes
			// `vm?.hci_info.disks ? ... : true`, and with no VM the optional
			// chain is undefined, so it falls through to true.
			//
			// The provider reports whichever endpoint it read from, which is
			// why the resource and the plural data source disagree in the
			// tests. Confirmed live on DEV, 2026-08-29.
			"isDetachable": true,
		})
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeVolumeAPI) snapshots(w http.ResponseWriter, id string) {
	v := f.get(w, id)
	if v == nil {
		return
	}
	out := []map[string]any{}
	for _, s := range v.snapshots {
		out = append(out, map[string]any{
			"id":            s.ID,
			"name":          s.Name,
			"status":        s.Status,
			"description":   s.Description,
			"size":          s.Size,
			"storagePolicy": s.StoragePolicy,
			"createdOn":     acctest.FakeCreatedAt,
		})
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}

func (f *fakeVolumeAPI) update(w http.ResponseWriter, r *http.Request, id string) {
	v := f.get(w, id)
	if v == nil {
		return
	}

	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "malformed body"})
		return
	}
	// updateVolumeSchema: both optional, both subject to the no-HTML rule.
	for field, value := range map[string]string{"name": body.Name, "description": body.Description} {
		if value != "" && !noHTML.MatchString(value) {
			validationError(w, fmt.Sprintf(
				"Invalid characters in '%s'. HTML tags and special characters (<, >, &, ', \") are not allowed.", field))
			return
		}
	}

	f.updates++
	if body.Name != "" {
		v.Name = body.Name
	}
	// Recorded but never reported back, exactly like the real thing.
	if body.Description != "" {
		v.Description = body.Description
	}

	acctest.WriteJSON(w, http.StatusOK, map[string]any{
		"id": v.ID, "status": v.Status, "size": v.Size,
		"created_at": acctest.FakeCreatedAt, "updated_at": acctest.FakeCreatedAt,
		"name": v.Name, "description": v.Description, "volume_type": v.StoragePolicy,
	})
}

func (f *fakeVolumeAPI) actions(w http.ResponseWriter, r *http.Request, id string) {
	v := f.get(w, id)
	if v == nil {
		return
	}

	var body struct {
		OsExtend *struct {
			NewSize int `json:"new_size"`
		} `json:"osExtend"`
		OsRetype *struct {
			NewType string `json:"new_type"`
		} `json:"osRetype"`
		OsUploadImage json.RawMessage `json:"osUploadImage"`
		Revert        json.RawMessage `json:"revert"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "malformed body"})
		return
	}

	// actionsVolumeSchema ends in .or(...): at least one action key, or 406.
	if body.OsExtend == nil && body.OsRetype == nil && body.OsUploadImage == nil && body.Revert == nil {
		validationError(w, "must contain at least one of 'osExtend', 'osDetach', 'osAttach', 'osForceDetach', 'osUploadImage', 'revert', 'osRetype'")
		return
	}

	switch {
	case body.OsExtend != nil:
		if body.OsExtend.NewSize <= v.Size {
			acctest.WriteJSON(w, http.StatusInternalServerError,
				map[string]any{"errorMessage": "New size for extend must be greater than current size"})
			return
		}
		f.extends++
		// The trap this fake exists to spring: the status does not move at all
		// and the volume goes on reporting its old size for several reads. A
		// waiter that only watches the status returns straight away with the
		// wrong size, and the apply then fails on an inconsistent result.
		//
		// Leaving the status alone is what the platform does. Observed live on
		// DEV: an extend on an attached volume was accepted with a 200 and the
		// status stayed `in-use` throughout — it never passed through
		// `available`, which is why volumeSettled has to accept both.
		v.pendingSize = body.OsExtend.NewSize
		v.sizeDelay = valueSettleReads

	case body.OsRetype != nil:
		if !f.validPolicy(body.OsRetype.NewType) {
			acctest.WriteJSON(w, http.StatusInternalServerError,
				map[string]any{"errorMessage": fmt.Sprintf("volume type %q not found", body.OsRetype.NewType)})
			return
		}
		f.retypes++
		// Same trap, on the policy.
		v.pendingPolicy = body.OsRetype.NewType
		v.policyDelay = valueSettleReads
	}

	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "ok"})
}

func (f *fakeVolumeAPI) delete(w http.ResponseWriter, id string) {
	v := f.get(w, id)
	if v == nil {
		return
	}

	// An in-use volume cannot be deleted. Observed live on DEV, 2026-09-01:
	// the request comes back 400 and the volume is untouched.
	//
	// The body is copied from that response rather than invented, down to the
	// double space where Cinder's format string failed to interpolate the
	// volume id, and the list of five conditions that never says which one
	// applied. That unhelpfulness is the reason the provider reads the volume
	// and writes its own message instead of passing this one through.
	if strings.EqualFold(v.Status, "in-use") {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{
				"badRequest": map[string]any{
					"code": 400,
					"message": "Invalid volume: Volume  must not be migrating, attached, belong to " +
						"a group, have snapshots or be disassociated from snapshots after volume transfer.",
				},
			},
			"code": "SERVER_ERROR",
		})
		return
	}

	// deleteById(volumeId, { cascade: true }) — the snapshots go too.
	v.snapshots = nil
	v.Status = "deleting"
	v.deleteDelay = settleReads
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}

// attachTo puts a volume into the state the API reports once it is attached to
// a VM: `in-use`, carrying the VM's identity, and detachable.
//
// No VM exists here and none is needed. This package has no code that attaches
// anything — attaching is dtcloud_vm_volume_attachment's job and is tested in
// the vm package — it only *reads* the attachment state the details endpoint
// reports. Putting a volume into that state exercises every line here that
// touches it, while keeping this fake to the routes it actually owns.
//
// What it deliberately cannot prove is that the vm package's attach really
// produces this shape. That is a contract between two fakes, and only a live
// run against a real VM settles it.
func (f *fakeVolumeAPI) attachTo(name, vmName, vmID, vmStatus string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.volumes {
		if v.Name == name && !v.deleted {
			v.Status = "in-use"
			v.AttachedTo = vmName
			v.AttachedToID = vmID
			v.AttachedToStatus = vmStatus
		}
	}
}

// detach returns a volume to `available`. Required before it can be deleted —
// see delete().
//
// The real thing passes through `detaching` on the way, and does not always
// arrive: observed live on DEV, a guest still holding the filesystem left the
// volume in `detaching` for about two and a half minutes and then put it back
// to `in-use`. That failure mode belongs to dtcloud_vm_volume_attachment, which
// owns detaching, so it is not modelled here — this package never detaches
// anything.
func (f *fakeVolumeAPI) detach(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.volumes {
		if v.Name == name && !v.deleted {
			v.Status = "available"
			v.AttachedTo = ""
			v.AttachedToID = ""
			v.AttachedToStatus = ""
		}
	}
}

// growOutOfBand resizes a volume the way the web console would — immediately,
// with no action call from the provider and no settle delay. It stands in for
// somebody growing a disk outside Terraform, which is what leaves the
// configuration behind reality.
func (f *fakeVolumeAPI) growOutOfBand(name string, size int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.volumes {
		if v.Name == name && !v.deleted {
			v.Size = size
		}
	}
}

// seed puts a volume in place without going through create, for tests that need
// something to clone from or to snapshot.
func (f *fakeVolumeAPI) seed(name, policy string, size int, snapshots ...fakeSnapshot) *fakeVolume {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := &fakeVolume{
		ID: f.nextID("vol"), Name: name, StoragePolicy: policy, Size: size,
		Status: "available", Type: "HDD", Bootable: "false", snapshots: snapshots,
	}
	f.volumes[v.ID] = v
	return v
}
