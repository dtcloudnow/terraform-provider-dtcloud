package image_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
)

// noHTML is the character rule the API applies to an image name.
var noHTML = regexp.MustCompile(`^[^<>&"']*$`)

// settleReads is how many reads a capture takes to finish. Above three, so a
// waiter watching the wrong thing cannot pass by accident.
const settleReads = 4

// valueSettleReads is the same for a field the update endpoint patches.
const valueSettleReads = 4

// capturedBytes is the size every capture produces: 3,000,000 bytes formats as
// "3 MB" exactly, so nothing is rounded.
const capturedBytes = 3000000

// allowedDiskFormats and allowedOsDistros are what the fake checks against.
// The provider does no checking of its own and lets the API refuse.
var (
	// What the CAPTURE endpoint takes, which is strictly fewer formats than the
	// old upload endpoint did - `iso` is not among them.
	allowedDiskFormats = map[string]bool{
		"raw": true, "vmdk": true, "vdi": true, "qcow2": true,
		"vhd": true, "vhdx": true, "ploop": true,
	}
	// Only the capture checks visibility, and `public` is refused by policy
	// rather than by validation.
	allowedVisibility = map[string]bool{"private": true, "shared": true, "community": true}
	// The four paths update accepts.
	allowedPatchPaths = map[string]bool{"/name": true, "/os_distro": true, "/min_disk": true, "/visibility": true}
)

type pendingValue struct {
	value interface{}
	delay int
}

type fakeImage struct {
	ID         string
	Name       string
	OsDistro   string
	OsType     string
	DiskFormat string
	MinDisk    int
	Visibility string
	Uefi       bool
	Status     string
	// Zero until the file arrives, which is what makes a new image report "0 MB".
	Bytes int64

	// Pending status change, counted down in details reads.
	pendingStatus string
	statusDelay   int

	// Pending field changes, keyed by patch path, each with its own countdown.
	pending map[string]*pendingValue

	deleted     bool
	deleteDelay int
}

// tick advances every pending change by one read. They advance together, so
// waiting on one cannot settle another.
func (i *fakeImage) tick() {
	if i.deleteDelay > 0 {
		i.deleteDelay--
		if i.deleteDelay == 0 {
			i.deleted = true
		}
	}
	if i.statusDelay > 0 {
		i.statusDelay--
		if i.statusDelay == 0 {
			i.Status = i.pendingStatus
		}
	}
	for path, p := range i.pending {
		p.delay--
		if p.delay > 0 {
			continue
		}
		switch path {
		case "/name":
			i.Name = p.value.(string)
		case "/os_distro":
			i.OsDistro = p.value.(string)
		case "/visibility":
			i.Visibility = p.value.(string)
		case "/min_disk":
			i.MinDisk = int(p.value.(float64))
		}
		delete(i.pending, path)
	}
}

// detailsImage is a struct so the field order is the API's: a field that fails
// to decode takes everything declared after it with it.
type detailsImage struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Status        string      `json:"status"`
	Size          string      `json:"size"`
	OsType        interface{} `json:"osType,omitempty"`
	MinVolumeSize string      `json:"minVolumeSize"`
	Type          string      `json:"type"`
	Visibility    string      `json:"visibility"`
	OsDistro      string      `json:"os_distro"`
	Uefi          bool        `json:"uefi"`
}

// createdImageBody is what create answers with: the raw image object, `id` well
// down the response rather than at the front.
type createdImageBody struct {
	OsDistro        string      `json:"os_distro"`
	Name            string      `json:"name"`
	DiskFormat      string      `json:"disk_format"`
	ContainerFormat string      `json:"container_format"`
	Visibility      string      `json:"visibility"`
	Size            interface{} `json:"size"`
	VirtualSize     interface{} `json:"virtual_size"`
	Status          string      `json:"status"`
	Checksum        interface{} `json:"checksum"`
	Protected       bool        `json:"protected"`
	MinRAM          int         `json:"min_ram"`
	MinDisk         int         `json:"min_disk"`
	Owner           string      `json:"owner"`
	OsHidden        bool        `json:"os_hidden"`
	OsHashAlgo      interface{} `json:"os_hash_algo"`
	OsHashValue     interface{} `json:"os_hash_value"`
	ID              string      `json:"id"`
	CreatedAt       string      `json:"created_at"`
	UpdatedAt       string      `json:"updated_at"`
	Tags            []string    `json:"tags"`
	Self            string      `json:"self"`
	File            string      `json:"file"`
	Schema          string      `json:"schema"`
}

// listImage is the same image as list reports it: a different distro key, and
// osType filled in.
type listImage struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Status        string      `json:"status"`
	Type          string      `json:"type"`
	OsType        interface{} `json:"osType,omitempty"`
	OsDistro      string      `json:"osDistro,omitempty"`
	MinVolumeSize string      `json:"minVolumeSize"`
	Size          string      `json:"size"`
	Visibility    string      `json:"visibility"`
	Uefi          bool        `json:"uefi"`
}

// notFound is the body these routes send for a missing image: an HTML page
// wrapped in a string, carrying its status only in the title.
func notFound(w http.ResponseWriter, id string) {
	acctest.WriteJSON(w, http.StatusNotFound, map[string]any{
		"error": fmt.Sprintf("<html>\n <head>\n  <title>404 Not Found</title>\n </head>\n"+
			" <body>\n  <h1>404 Not Found</h1>\n  No image found with ID %s<br /><br />\n\n\n\n"+
			" </body>\n</html>", id),
		"code": "SERVER_ERROR",
	})
}

// validationError reproduces the rejection body, leading space and trailing
// comma included.
func validationError(w http.ResponseWriter, message string) {
	acctest.WriteJSON(w, http.StatusNotAcceptable, map[string]any{
		"error": " " + message + ",",
		"code":  "VALIDATE_ERROR",
	})
}

func badRequest(w http.ResponseWriter, message string) {
	acctest.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"error": message,
		"code":  "BAD_REQUEST",
	})
}

// formatSize renders a byte count as the API does: GB above a billion bytes,
// whole MB below.
func formatSize(bytes int64) string {
	if bytes >= 1000000000 {
		return fmt.Sprintf("%.1f GB", float64(bytes)/1000000000)
	}
	return fmt.Sprintf("%.0f MB", float64(bytes)/1000000)
}

// formatMinVolumeSize renders min_disk as text, with a dash for "none".
func formatMinVolumeSize(minDisk int) string {
	if minDisk == 0 {
		return "-"
	}
	return fmt.Sprintf("%d GB", minDisk)
}

// displayType is the category derived from the disk format, not the format.
func displayType(diskFormat string) string {
	if diskFormat == "iso" {
		return "ISO"
	}
	return "Template (VM)"
}

// derivedOsType is the guess list makes when os_type is missing.
func derivedOsType(osType, osDistro string) interface{} {
	if osType != "" {
		return osType
	}
	if osDistro == "" {
		return nil
	}
	if strings.Contains(strings.ToLower(osDistro), "win") {
		return "windows"
	}
	return "linux"
}

// fakeImageAPI stands in for the images endpoints.
type fakeImageAPI struct {
	mu     sync.Mutex
	images map[string]*fakeImage
	seq    int

	captures int
	updates  int
	deletes  int
	detailsN int

	// patchPaths records every path patched, in order.
	patchPaths []string

	// Together these say whether the provider polled the delete through to the end.
	readsAfterDelete int
	servedGone       int

	// volumes are the capture sources. A volume that is not `available` makes
	// the capture fail the way the platform does.
	volumes map[string]*fakeVolume

	// versions is a separate catalogue from the images above.
	versions map[string][]map[string]any
}

func newFakeImageAPI() *fakeImageAPI {
	return &fakeImageAPI{
		images:  map[string]*fakeImage{},
		volumes: map[string]*fakeVolume{},
		versions: map[string][]map[string]any{
			// A display category, not an operating system.
			"ubuntu": {
				// The column is untyped: a read must not fail on a value it did not write.
				{"id": "img-cat-0001", "version": "20.04", "type": "Template (VM)", "minVolumeSize": 20,
					"validFlavorIds": []any{"flavor-1", "flavor-2"}},
				// Null, which is what every entry really holds.
				{"id": "img-cat-0002", "version": "22.04", "type": "Template (VM)", "minVolumeSize": 25,
					"validFlavorIds": nil},
			},
			"windows": {
				// A comma-separated string, the other shape the column can take.
				{"id": "img-cat-0003", "version": "2019", "type": "ISO", "minVolumeSize": 50,
					"validFlavorIds": "flavor-3, flavor-4"},
			},
		},
	}
}

func (f *fakeImageAPI) nextID() string {
	f.seq++
	return fmt.Sprintf("img-%04d", f.seq)
}

// seed adds an image the provider did not create.
func (f *fakeImageAPI) seed(img *fakeImage) *fakeImage {
	if img.ID == "" {
		img.ID = f.nextID()
	}
	if img.pending == nil {
		img.pending = map[string]*pendingValue{}
	}
	f.images[img.ID] = img
	return img
}

// splitPath drops the empty segments a leading or trailing slash leaves behind.
func splitPath(path string) []string {
	seg := []string{}
	for _, s := range strings.Split(strings.Trim(path, "/"), "/") {
		if s != "" {
			seg = append(seg, s)
		}
	}
	return seg
}

func (f *fakeImageAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !acctest.RequireAuth(w, r) {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// The capture lives on the VOLUME, so this fake has to serve both trees.
	if volPath := strings.TrimPrefix(r.URL.Path, "/openstack/volumes"); volPath != r.URL.Path {
		vseg := splitPath(volPath)
		switch {
		case r.Method == http.MethodGet && len(vseg) == 2 && vseg[1] == "details":
			f.volumeDetails(w, vseg[0])
		case r.Method == http.MethodPost && len(vseg) == 2 && vseg[1] == "actions":
			f.capture(w, r, vseg[0])
		default:
			notFound(w, strings.Trim(volPath, "/"))
		}
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/openstack/images")
	seg := splitPath(path)

	switch {
	case r.Method == http.MethodGet && len(seg) == 0:
		f.list(w)
	case r.Method == http.MethodGet && len(seg) == 1 && seg[0] == "versions":
		acctest.WriteJSON(w, http.StatusOK, f.versions)
	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "details":
		f.details(w, seg[0])
	case r.Method == http.MethodPut && len(seg) == 1:
		f.update(w, r, seg[0])
	case r.Method == http.MethodDelete && len(seg) == 1:
		f.delete(w, seg[0])
	default:
		notFound(w, strings.Trim(path, "/"))
	}
}

func (f *fakeImageAPI) get(w http.ResponseWriter, id string) *fakeImage {
	img, ok := f.images[id]
	if !ok || img.deleted {
		if ok {
			f.servedGone++
		}
		notFound(w, id)
		return nil
	}
	return img
}

// fakeVolume is a capture source. Only the status matters here.
type fakeVolume struct {
	ID     string
	Name   string
	Size   int
	Status string
}

// seedVolume adds a volume the capture can be pointed at.
func (f *fakeImageAPI) seedVolume(v *fakeVolume) *fakeVolume {
	if v.ID == "" {
		// A real volume id is a UUID, and the resource validates the shape during
		// plan, so a "vol-0001" here would fail for the wrong reason.
		f.seq++
		v.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", f.seq)
	}
	if v.Status == "" {
		v.Status = "available"
	}
	f.volumes[v.ID] = v
	return v
}

// volumeDetails answers the availability check the provider makes before it
// asks for a capture.
func (f *fakeImageAPI) volumeDetails(w http.ResponseWriter, id string) {
	vol, ok := f.volumes[id]
	if !ok {
		notFound(w, id)
		return
	}
	acctest.WriteJSON(w, http.StatusOK, map[string]any{
		"id":     vol.ID,
		"name":   vol.Name,
		"size":   vol.Size,
		"status": vol.Status,
	})
}

// capture is POST /openstack/volumes/{id}/actions with an osUploadImage body.
//
// Two behaviours here are the whole reason the provider works the way it does:
// the response carries NO id - 200 and an empty body - so the caller has to
// find the new image in the list; and a volume that is not `available` is
// refused outright rather than queued.
func (f *fakeImageAPI) capture(w http.ResponseWriter, r *http.Request, volumeID string) {
	vol, ok := f.volumes[volumeID]
	if !ok {
		notFound(w, volumeID)
		return
	}

	var body struct {
		OsUploadImage *struct {
			ImageName       string `json:"image_name"`
			DiskFormat      string `json:"disk_format"`
			ContainerFormat string `json:"container_format"`
			Visibility      string `json:"visibility"`
		} `json:"osUploadImage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "malformed body")
		return
	}
	if body.OsUploadImage == nil {
		badRequest(w, "osUploadImage is required")
		return
	}
	p := body.OsUploadImage

	if vol.Status != "available" {
		badRequest(w, fmt.Sprintf("Invalid volume: Volume %s status must be available", volumeID))
		return
	}
	if p.ImageName == "" {
		validationError(w, "'image_name' is required")
		return
	}
	if !noHTML.MatchString(p.ImageName) {
		validationError(w, "Invalid characters in 'name'. HTML tags and special characters (<, >, &, ', \") are not allowed.")
		return
	}
	if !allowedDiskFormats[p.DiskFormat] {
		badRequest(w, fmt.Sprintf(
			"Invalid input for field/attribute disk_format. Value: %s. '%s' is not one of "+
				"['raw', 'vmdk', 'vdi', 'qcow2', 'vhd', 'vhdx', 'ploop']", p.DiskFormat, p.DiskFormat))
		return
	}
	visibility := p.Visibility
	if visibility == "" {
		visibility = "shared"
	}
	if visibility == "public" {
		// Policy, not validation - and the body is an HTML page wrapped in JSON.
		acctest.WriteJSON(w, http.StatusForbidden, map[string]any{
			"error": map[string]any{"forbidden": map[string]any{"code": 403,
				"message": "Policy doesn't allow volume_extension:volume_actions:upload_public to be performed."}},
			"code": "SERVER_ERROR",
		})
		return
	}
	if !allowedVisibility[visibility] {
		validationError(w, "'visibility' must be one of [private, shared, community]")
		return
	}

	f.captures++
	img := &fakeImage{
		ID:   f.nextID(),
		Name: p.ImageName,
		// Both are inherited from the volume, not taken from the request.
		OsDistro:   "debian12",
		MinDisk:    vol.Size,
		DiskFormat: p.DiskFormat,
		Visibility: visibility,
		Bytes:      capturedBytes,
		// Unlike an uploaded image, a captured one carries os_type: the platform
		// works it out from the volume rather than leaving it blank.
		OsType: "linux",
		Status: "queued",
		// The copy runs after the call returns, so the image is not usable yet.
		pendingStatus: "active",
		statusDelay:   settleReads,
		pending:       map[string]*pendingValue{},
	}
	f.images[img.ID] = img

	// 200, empty body, no Location header. Nothing names the image it just made.
	w.WriteHeader(http.StatusOK)
}

func (f *fakeImageAPI) detailsView(img *fakeImage) detailsImage {
	return detailsImage{
		ID:     img.ID,
		Name:   img.Name,
		Status: img.Status,
		Size:   formatSize(img.Bytes),
		// Details reports whatever is held, which for an uploaded image is nothing.
		OsType:        nilIfEmpty(img.OsType),
		MinVolumeSize: formatMinVolumeSize(img.MinDisk),
		Type:          displayType(img.DiskFormat),
		Visibility:    img.Visibility,
		OsDistro:      img.OsDistro,
		Uefi:          img.Uefi,
	}
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func (f *fakeImageAPI) details(w http.ResponseWriter, id string) {
	img := f.get(w, id)
	if img == nil {
		return
	}
	f.detailsN++
	if img.deleteDelay > 0 {
		f.readsAfterDelete++
	}
	view := f.detailsView(img)
	// Reported before the tick, so a delay of n really is invisible for n reads.
	img.tick()
	acctest.WriteJSON(w, http.StatusOK, view)
}

func (f *fakeImageAPI) list(w http.ResponseWriter) {
	// No filter and no sort: everything comes back in insertion order.
	out := []listImage{}
	for i := 1; i <= f.seq; i++ {
		img, ok := f.images[fmt.Sprintf("img-%04d", i)]
		if !ok || img.deleted {
			continue
		}
		out = append(out, listImage{
			ID:     img.ID,
			Name:   img.Name,
			Status: img.Status,
			Type:   displayType(img.DiskFormat),
			// The guess the details endpoint does not make.
			OsType: derivedOsType(img.OsType, img.OsDistro),
			// And under a different key from the details endpoint's.
			OsDistro:      img.OsDistro,
			MinVolumeSize: formatMinVolumeSize(img.MinDisk),
			Size:          formatSize(img.Bytes),
			Visibility:    img.Visibility,
			Uefi:          img.Uefi,
		})
	}
	acctest.WriteJSON(w, http.StatusOK, out)
}

// update patches one field; the endpoint has no way to take two.
func (f *fakeImageAPI) update(w http.ResponseWriter, r *http.Request, id string) {
	img := f.get(w, id)
	if img == nil {
		return
	}

	var body struct {
		Op    string      `json:"op"`
		Path  string      `json:"path"`
		Value interface{} `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "malformed body")
		return
	}

	if body.Op != "add" && body.Op != "replace" {
		validationError(w, "'op' must be one of [add, replace]")
		return
	}
	if !allowedPatchPaths[body.Path] {
		validationError(w, "'path' must be one of [/name, /os_distro, /min_disk, /visibility]")
		return
	}
	if body.Value == nil {
		validationError(w, "'value' is required")
		return
	}

	switch body.Path {
	case "/name":
		name, ok := body.Value.(string)
		if !ok || name == "" {
			validationError(w, "'name' must not be empty or contain only spaces")
			return
		}
		if !noHTML.MatchString(name) {
			validationError(w, "Invalid characters in 'name'. HTML tags and special characters (<, >, &, ', \") are not allowed.")
			return
		}
	case "/min_disk":
		// Checked by the platform, not by the update schema.
		size, ok := body.Value.(float64)
		if !ok || size < 1 || size > 512 {
			validationError(w, "'min_disk' must be between 1 and 512")
			return
		}
	case "/os_distro":
		// Deliberately unchecked. The platform validates os_distro nowhere on this
		// endpoint - a value it would refuse elsewhere is stored without complaint,
		// and the next plan is clean. Validating here would hide that.

	case "/visibility":
		visibility, _ := body.Value.(string)
		if visibility == "public" {
			// Policy, not validation, and the body is an HTML page.
			acctest.WriteJSON(w, http.StatusForbidden, map[string]any{
				"error": "<html>\n <head>\n  <title>403 Forbidden</title>\n </head>\n <body>\n" +
					"  <h1>403 Forbidden</h1>\n  You are not authorized to complete publicize_image action.<br /><br />\n</body>\n</html>",
				"code": "SERVER_ERROR",
			})
			return
		}
		if !allowedVisibility[visibility] {
			validationError(w, "'visibility' must be one of [private, shared, community]")
			return
		}
	}

	f.updates++
	f.patchPaths = append(f.patchPaths, body.Path)
	// The value takes several reads to appear, and the status does not move.
	img.pending[body.Path] = &pendingValue{value: body.Value, delay: valueSettleReads}

	// The raw object rather than a read shape, so state filled from it differs.
	acctest.WriteJSON(w, http.StatusOK, map[string]any{
		"id":          img.ID,
		"name":        img.Name,
		"disk_format": img.DiskFormat,
		"os_distro":   img.OsDistro,
		"min_disk":    img.MinDisk,
		"visibility":  img.Visibility,
		"status":      img.Status,
	})
}

// delete answers with no body, then lets the image linger for a few reads.
func (f *fakeImageAPI) delete(w http.ResponseWriter, id string) {
	img := f.get(w, id)
	if img == nil {
		return
	}
	f.deletes++
	img.deleteDelay = settleReads
	w.WriteHeader(http.StatusNoContent)
}
