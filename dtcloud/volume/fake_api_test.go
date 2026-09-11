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

// noHTML is the character rule the API applies to name and description.
var noHTML = regexp.MustCompile(`^[^<>&"']*$`)

// settleReads is how many details reads a status change takes to land.
const settleReads = 2

// valueSettleReads is the same for extend and retype, and larger: those leave
// the volume at its resting status, so a status-only waiter returns early.
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
	// Held but never reported back.
	Description string
	ImageID     string
	ImageName   string
	DiskFormat  string

	AttachedTo       string
	AttachedToID     string
	AttachedToStatus string

	// Pending changes, each with a countdown in details reads.
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
// the volume before calling this, and the counters advance together, so waiting
// on one change cannot settle another.
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

// imageMeta is present only on image-backed volumes; a pointer so it can be
// omitted rather than sent empty.
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

// detailsResponse is a struct so the field order is the API's: a field that
// fails to decode takes everything declared after it with it.
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

// fakeVolumeAPI stands in for the volume endpoints.
type fakeVolumeAPI struct {
	mu      sync.Mutex
	volumes map[string]*fakeVolume
	seq     int

	creates  int
	clones   int
	restores int
	extends  int
	retypes  int
	updates  int
	detailsN int

	policies []map[string]string

	restorableSnapshots map[string]restorableSnapshot
}

// restorableSnapshot is only the far end of the restore call.
type restorableSnapshot struct {
	ID   string
	Size int
}

func newFakeVolumeAPI() *fakeVolumeAPI {
	return &fakeVolumeAPI{
		volumes: map[string]*fakeVolume{},
		restorableSnapshots: map[string]restorableSnapshot{
			"snap-0001": {ID: "snap-0001", Size: 20},
		},
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

	f.mu.Lock()
	defer f.mu.Unlock()

	// source_snapshot_id goes to the snapshot service, and what comes back is the
	// raw volume object keyed `id` — the clone shape, from a third endpoint.
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/openstack/snapshots/") &&
		strings.HasSuffix(r.URL.Path, "/snapshot-to-volume") {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/openstack/snapshots/"), "/snapshot-to-volume")
		f.snapshotToVolume(w, r, id)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/openstack/volumes")
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
	// Declared before the {id} routes: a literal segment must not read as an id.
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

// validationError reproduces the API's rejection body.
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

	// Validation, in the order the API applies it.
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
		// Every non-target status has to count as pending.
		Status:        "creating",
		pendingStatus: "available",
		statusDelay:   settleReads,
	}
	if body.ImageRef != "" {
		v.Bootable = "true"
		v.ImageID = body.ImageRef
		v.ImageName = "image-" + body.ImageRef
		v.DiskFormat = "qcow2"
		// An image-backed volume passes through `downloading` too.
		v.Status = "downloading"
	}
	f.volumes[v.ID] = v
	f.creates++

	// The create response keys the id `volumeId`. Nothing here is called `id`.
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
	// Clone requires name, size and storagePolicy; unknown keys are rejected.
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

	// Clone returns the raw volume, keyed `id`, so the provider copes with both.
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

// snapshotToVolume restores into a new volume. The size is floored at the
// snapshot's: a volume cannot be smaller than the data restored into it.
func (f *fakeVolumeAPI) snapshotToVolume(w http.ResponseWriter, r *http.Request, snapshotID string) {
	snap, ok := f.restorableSnapshots[snapshotID]
	if !ok {
		acctest.NotFound(w, fmt.Sprintf("Snapshot %s could not be found.", snapshotID))
		return
	}

	var body struct {
		Name          string `json:"name"`
		Size          int    `json:"size"`
		StoragePolicy string `json:"storagePolicy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "malformed body"})
		return
	}
	if body.Name == "" || body.Size == 0 || body.StoragePolicy == "" {
		validationError(w, "'name', 'size' and 'storagePolicy' are required")
		return
	}
	if body.Size < snap.Size {
		acctest.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"errorMessage": fmt.Sprintf("Volume size %d is smaller than snapshot size %d", body.Size, snap.Size),
		})
		return
	}

	v := &fakeVolume{
		ID:            f.nextID("vol"),
		Name:          body.Name,
		StoragePolicy: body.StoragePolicy,
		Size:          body.Size,
		Type:          "HDD",
		Bootable:      "false",
		Status:        "creating",
		pendingStatus: "available",
		statusDelay:   settleReads,
	}
	f.volumes[v.ID] = v
	f.restores++

	// The raw volume, keyed `id`. Nothing here is called `volumeId`.
	acctest.WriteJSON(w, http.StatusOK, map[string]any{
		"id":          v.ID,
		"status":      v.Status,
		"size":        v.Size,
		"created_at":  acctest.FakeCreatedAt,
		"updated_at":  acctest.FakeCreatedAt,
		"name":        v.Name,
		"description": "",
		"volume_type": v.StoragePolicy,
		"snapshot_id": snap.ID,
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
		// A string, not a JSON boolean.
		Bootable:     v.Bootable,
		Created:      acctest.FakeCreatedAt,
		LastModified: acctest.FakeCreatedAt,
		Type:         v.Type,
		// false while detached, disagreeing with list on the very same volume.
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

	// Reported first, advanced after, so a pending change is never visible early.
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
			// Built as `size + ' GB'` here, and a number on the details route.
			"size":         fmt.Sprintf("%d GB", v.Size),
			"bootable":     v.Bootable,
			"attachedTo":   v.AttachedTo,
			"attachedToId": v.AttachedToID,
			"vmStatus":     v.AttachedToStatus,
			"type":         v.Type,
			// Always true for a detached volume, where details says false: the two
			// handlers compute it differently. The provider reports whichever it read.
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
	// Both optional, both subject to the character rule.
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

	// At least one action key, or the request is rejected.
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
		// The trap this fake exists to spring: the status does not move while the old
		// size is still reported. An extend on an attached volume stays `in-use`
		// throughout, which is why volumeSettled accepts both resting statuses.
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

	// An in-use volume cannot be deleted. The refusal lists five conditions and
	// never says which applied, so the provider checks first and writes its own.
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

// attachTo puts a volume into the state reported once it is attached to a VM.
// No VM exists and none is needed: this package only reads that state, it never
// attaches. What it cannot prove is that the vm package produces this shape.
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

// detach returns a volume to `available`, which is required before delete. The
// transitional state belongs to dtcloud_vm_volume_attachment, not here.
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

// growOutOfBand resizes a volume the way the web console would, standing in for
// somebody growing a disk outside Terraform.
func (f *fakeVolumeAPI) growOutOfBand(name string, size int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.volumes {
		if v.Name == name && !v.deleted {
			v.Size = size
		}
	}
}

// seed puts a volume in place without going through create.
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
