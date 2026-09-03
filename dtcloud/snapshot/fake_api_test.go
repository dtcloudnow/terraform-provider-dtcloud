package snapshot_test

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

// settleReads is how many reads a status change takes to land, so the create
// and delete waiters are exercised rather than satisfied on the first poll.
//
// It has to exceed three: a wait that watches the wrong thing ends after two
// reads, and the read that follows it costs a third. Below that threshold a
// waiter that returned far too early is indistinguishable from one that waited.
const settleReads = 4

// valueSettleReads is the same idea for a rename or a re-describe: the object
// sits at its resting status still reporting its old value, so a waiter
// watching only the status would return having done nothing.
//
// Larger than anything the platform actually does. The fake models the case the
// waiter has to survive rather than the one that happens to be fast today.
const valueSettleReads = 4

type fakeSnapshot struct {
	ID           string
	Name         string
	Description  string
	VolumeID     string
	VolumeTypeID string
	Status       string
	Size         int
	Progress     string
	ProjectID    string
	CreatedAt    string
	// Empty until the snapshot is first renamed or re-described; sent as null.
	UpdatedAt string

	// Pending changes, each with its own countdown in details reads.
	pendingStatus string
	statusDelay   int
	pendingName   string
	nameDelay     int
	pendingDesc   string
	descDelay     int

	deleted     bool
	deleteDelay int
}

// tick advances every pending change by one read. The caller reports the
// snapshot before calling this, so a change with a delay of n is invisible for
// n reads.
//
// The counters advance together rather than in turn, so that waiting for one
// change cannot settle another — which is how a wait that watches the wrong
// thing gets covered by one that watches the right thing.
func (s *fakeSnapshot) tick() {
	if s.deleteDelay > 0 {
		s.deleteDelay--
		if s.deleteDelay == 0 {
			s.deleted = true
		}
	}
	if s.statusDelay > 0 {
		s.statusDelay--
		if s.statusDelay == 0 {
			s.Status = s.pendingStatus
			// The copy finishing and the progress reaching 100% are one event.
			if s.Status == "available" {
				s.Progress = "100%"
			}
		} else {
			s.Progress = fmt.Sprintf("%d%%", 100-(s.statusDelay*100/settleReads))
		}
	}
	if s.nameDelay > 0 {
		s.nameDelay--
		if s.nameDelay == 0 {
			s.Name = s.pendingName
		}
	}
	if s.descDelay > 0 {
		s.descDelay--
		if s.descDelay == 0 {
			s.Description = s.pendingDesc
		}
	}
}

// detailsSnapshot is a struct rather than a map on purpose: Go sorts map keys
// alphabetically, which would move created_at away from the front and hide the
// class of bug where a field that fails to decode takes everything declared
// after it with it. The field order here matches the API's.
type detailsSnapshot struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	UpdatedAt any    `json:"updated_at"`
	Name      string `json:"name"`
	// `any`, because an unset description arrives as JSON null rather than as
	// an empty string. That difference is the point of the clear-by-null path
	// in update below.
	Description  any    `json:"description"`
	VolumeID     string `json:"volume_id"`
	VolumeTypeID string `json:"volume_type_id"`
	Status       string `json:"status"`
	Size         int    `json:"size"`
	// Always empty, and nothing reads it. Present because the API sends it, and
	// a fixture that drops fields stops being a copy of the API.
	Metadata  map[string]any `json:"metadata"`
	ProjectID string         `json:"os-extended-snapshot-attributes:project_id"`
	Progress  string         `json:"os-extended-snapshot-attributes:progress"`
}

// listSnapshot is the same object as the list endpoint reports it. The two
// extended attributes are absent there, which is why the plural data source
// does not expose them.
type listSnapshot struct {
	ID           string         `json:"id"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    any            `json:"updated_at"`
	Name         string         `json:"name"`
	Description  any            `json:"description"`
	VolumeID     string         `json:"volume_id"`
	VolumeTypeID string         `json:"volume_type_id"`
	Status       string         `json:"status"`
	Size         int            `json:"size"`
	Metadata     map[string]any `json:"metadata"`
}

// notFound writes the shape the API sends for a missing object.
//
// Deliberately not acctest.NotFound, which writes a flat message body. The real
// 404 carries a nested status code, and reproducing it here exercises the
// structured branch of dterr.IsNotFound rather than its text fallback.
func notFound(w http.ResponseWriter, message string) {
	acctest.WriteJSON(w, http.StatusNotFound, map[string]any{
		"error": map[string]any{
			"itemNotFound": map[string]any{
				"code":    404,
				"message": message,
			},
		},
		"code": "SERVER_ERROR",
	})
}

// validationError reproduces the API's rejection body, including the leading
// space and trailing comma the real one carries.
func validationError(w http.ResponseWriter, message string) {
	acctest.WriteJSON(w, http.StatusNotAcceptable, map[string]any{
		"error": " " + message + ",",
		"code":  "VALIDATE_ERROR",
	})
}

// fakeSnapshotAPI stands in for the snapshot endpoints, plus the one volumes
// endpoint this package reads.
//
// The rules it enforces are the API's, not the ones the provider would find
// convenient. In particular it reproduces:
//
//   - the list reporting a volume type id where the rest of the provider
//     reports a policy name;
//   - create accepting no description, so one needs a follow-up update;
//   - an empty name or description being rejected, while a null description
//     clears the field;
//   - a rename sitting at `available` with the old value for several reads;
//   - created_at near the front of the response, ahead of everything that
//     would be lost if it failed to decode;
//   - updated_at arriving as null on a snapshot nobody has renamed;
//   - delete answering with a bare status and no body.
type fakeSnapshotAPI struct {
	mu        sync.Mutex
	snapshots map[string]*fakeSnapshot
	seq       int

	creates  int
	updates  int
	deletes  int
	detailsN int
	restores int

	// Volume ids create will accept. What matters is that an unknown one is
	// rejected at all.
	volumes map[string]int

	policies []map[string]string

	// policiesFail makes the storage policies unreadable, so the best-effort
	// lookup can be shown to degrade rather than fail a read.
	policiesFail bool
}

func newFakeSnapshotAPI() *fakeSnapshotAPI {
	return &fakeSnapshotAPI{
		snapshots: map[string]*fakeSnapshot{},
		volumes:   map[string]int{"vol-0001": 20, "vol-0002": 40},
		policies: []map[string]string{
			{"id": "type-0001", "name": "standard"},
			{"id": "type-0002", "name": "fast"},
		},
	}
}

func (f *fakeSnapshotAPI) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%04d", prefix, f.seq)
}

func (f *fakeSnapshotAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !acctest.RequireAuth(w, r) {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// The provider reads one volumes endpoint: storage policies, to turn a
	// volume type id into a policy name.
	if r.URL.Path == "/openstack/volumes/storage-policies" && r.Method == http.MethodGet {
		if f.policiesFail {
			acctest.WriteJSON(w, http.StatusInternalServerError,
				map[string]any{"errorMessage": "Server error"})
			return
		}
		acctest.WriteJSON(w, http.StatusOK, f.policies)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/openstack/snapshots")
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
	case r.Method == http.MethodPost && len(seg) == 2 && seg[1] == "snapshot-to-volume":
		f.snapshotToVolume(w, r, seg[0])
	case r.Method == http.MethodPut && len(seg) == 1:
		f.update(w, r, seg[0])
	case r.Method == http.MethodDelete && len(seg) == 1:
		f.delete(w, seg[0])
	default:
		notFound(w, "no such route")
	}
}

func (f *fakeSnapshotAPI) get(w http.ResponseWriter, id string) *fakeSnapshot {
	s, ok := f.snapshots[id]
	if !ok || s.deleted {
		notFound(w, fmt.Sprintf("Snapshot %s could not be found.", id))
		return nil
	}
	return s
}

func (f *fakeSnapshotAPI) create(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "malformed body"})
		return
	}

	name, _ := body["name"].(string)
	volumeID, _ := body["volume_id"].(string)

	// Validation, in the order the API applies it.
	if name == "" {
		validationError(w, "'name' is required")
		return
	}
	if !noHTML.MatchString(name) {
		validationError(w, "Invalid characters in 'name'. HTML tags and special characters (<, >, &, ', \") are not allowed.")
		return
	}
	if volumeID == "" {
		validationError(w, "'volume_id' is required")
		return
	}
	size, ok := f.volumes[volumeID]
	if !ok {
		notFound(w, fmt.Sprintf("Volume %s could not be found.", volumeID))
		return
	}

	// Create accepts no description at all. Failing loudly here is what forces
	// the provider to make the follow-up call instead of hoping this one took.
	if _, sent := body["description"]; sent {
		panic("the provider sent a description to create; the API accepts none")
	}

	s := &fakeSnapshot{
		ID:           f.nextID("snap"),
		Name:         name,
		VolumeID:     volumeID,
		VolumeTypeID: "type-0001",
		// Size is the source volume's, not something the caller chose.
		Size:      size,
		ProjectID: "proj-0001",
		CreatedAt: acctest.FakeCreatedAt,
		// Fresh snapshots are `creating` while the copy runs, with the progress
		// counting up behind it.
		Status:        "creating",
		Progress:      "0%",
		pendingStatus: "available",
		statusDelay:   settleReads,
	}
	f.snapshots[s.ID] = s
	f.creates++

	// The created object comes back wrapped.
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"snapshot": f.detailsView(s)})
}

// orNull renders an empty string as JSON null, which is what the API sends for
// a description or an updated_at that has never been written.
func orNull(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (f *fakeSnapshotAPI) detailsView(s *fakeSnapshot) detailsSnapshot {
	return detailsSnapshot{
		ID:           s.ID,
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    orNull(s.UpdatedAt),
		Name:         s.Name,
		Description:  orNull(s.Description),
		VolumeID:     s.VolumeID,
		VolumeTypeID: s.VolumeTypeID,
		Status:       s.Status,
		Size:         s.Size,
		Metadata:     map[string]any{},
		ProjectID:    s.ProjectID,
		Progress:     s.Progress,
	}
}

// updateView is what an update answers with: the details object minus the two
// extended attributes, the same width as the list endpoint.
func (f *fakeSnapshotAPI) updateView(s *fakeSnapshot) listSnapshot {
	return listSnapshot{
		ID:           s.ID,
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    orNull(s.UpdatedAt),
		Name:         s.Name,
		Description:  orNull(s.Description),
		VolumeID:     s.VolumeID,
		VolumeTypeID: s.VolumeTypeID,
		Status:       s.Status,
		Size:         s.Size,
		Metadata:     map[string]any{},
	}
}

func (f *fakeSnapshotAPI) details(w http.ResponseWriter, id string) {
	s := f.get(w, id)
	if s == nil {
		return
	}
	f.detailsN++
	view := f.detailsView(s)
	// Reported before the tick, so a change with a delay of n really is
	// invisible for n reads.
	s.tick()
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"snapshot": view})
}

func (f *fakeSnapshotAPI) list(w http.ResponseWriter) {
	// The list endpoint takes no filter and no sort: everything visible comes
	// back in insertion order, and filtering is the provider's problem.
	out := []listSnapshot{}
	for i := 1; i <= f.seq; i++ {
		s, ok := f.snapshots[fmt.Sprintf("snap-%04d", i)]
		if !ok || s.deleted {
			continue
		}
		out = append(out, listSnapshot{
			ID:          s.ID,
			CreatedAt:   s.CreatedAt,
			UpdatedAt:   orNull(s.UpdatedAt),
			Name:        s.Name,
			Description: orNull(s.Description),
			VolumeID:    s.VolumeID,
			// An id, not a name — the reason the provider makes a second call.
			VolumeTypeID: s.VolumeTypeID,
			Status:       s.Status,
			Size:         s.Size,
			Metadata:     map[string]any{},
		})
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"snapshots": out})
}

func (f *fakeSnapshotAPI) update(w http.ResponseWriter, r *http.Request, id string) {
	s := f.get(w, id)
	if s == nil {
		return
	}

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{"errorMessage": "malformed body"})
		return
	}

	// Three distinct requests, and the difference between the last two is why
	// dt-go needs an explicit way to send a null:
	//
	//	field absent -> leave it alone
	//	""           -> rejected
	//	null         -> cleared

	if raw, sent := body["name"]; sent {
		name, ok := raw.(string)
		if !ok || name == "" {
			validationError(w, "'name' must not be empty or contain only spaces")
			return
		}
		if !noHTML.MatchString(name) {
			validationError(w, "Invalid characters in 'name'. HTML tags and special characters (<, >, &, ', \") are not allowed.")
			return
		}
		if name != s.Name {
			s.pendingName = name
			s.nameDelay = valueSettleReads
		}
	}
	if raw, sent := body["description"]; sent {
		if raw == nil {
			// A null clears it immediately: there is no new value to converge
			// on, so the settle delay does not apply.
			s.Description = ""
			s.pendingDesc, s.descDelay = "", 0
			goto described
		}
		desc, ok := raw.(string)
		if !ok || desc == "" {
			validationError(w, "'description' must not be empty or contain only spaces")
			return
		}
		if !noHTML.MatchString(desc) {
			validationError(w, "Invalid characters in 'description'. HTML tags and special characters (<, >, &, ', \") are not allowed.")
			return
		}
		if desc != s.Description {
			s.pendingDesc = desc
			s.descDelay = valueSettleReads
		}
	}
described:

	s.UpdatedAt = "2026-07-09T11:02:44.120931"
	f.updates++

	// The response carries the object as it is now, which with the settle delay
	// pending means the old values — harsher than the real API, so a provider
	// that trusted this body instead of re-reading is caught here.
	acctest.WriteJSON(w, http.StatusOK, map[string]any{"snapshot": f.updateView(s)})
}

func (f *fakeSnapshotAPI) delete(w http.ResponseWriter, id string) {
	s := f.get(w, id)
	if s == nil {
		return
	}
	f.deletes++
	s.Status = "deleting"
	s.deleteDelay = settleReads
	// A bare status code, no body at all.
	w.WriteHeader(http.StatusOK)
}

// snapshotToVolume is the restore endpoint. The provider reaches it through
// source_snapshot_id on dtcloud_volume, so nothing in this package calls it; it
// is here so a misrouted request fails visibly.
func (f *fakeSnapshotAPI) snapshotToVolume(w http.ResponseWriter, r *http.Request, id string) {
	s := f.get(w, id)
	if s == nil {
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
	f.restores++
	// The raw volume object, keyed `id` rather than `volumeId`.
	acctest.WriteJSON(w, http.StatusOK, map[string]any{
		"id":          f.nextID("vol"),
		"name":        body.Name,
		"status":      "creating",
		"size":        body.Size,
		"volume_type": body.StoragePolicy,
		"snapshot_id": s.ID,
	})
}
