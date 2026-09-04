package image_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
)

// noHTML is the character rule the API applies to an image name.
var noHTML = regexp.MustCompile(`^[^<>&"']*$`)

// settleReads is how many reads it takes for an upload to finish and for a
// deleted image to disappear, so the create and delete waiters are exercised
// rather than satisfied on the first poll.
//
// It has to exceed three: a wait that watches the wrong thing ends after two
// reads, and the read that follows it costs a third. Below that threshold a
// waiter that returned far too early is indistinguishable from one that waited.
const settleReads = 4

// valueSettleReads is the same idea for the four fields the update endpoint
// patches. The image sits at `active` the whole time still reporting its old
// value, so a waiter watching only the status would return having done nothing.
//
// Larger than anything the platform actually does. The fake models the case the
// waiter has to survive rather than the one that happens to be fast today.
const valueSettleReads = 4

// defaultMaxUploadBytes stands in for the platform's configured upload ceiling.
// Smaller than the real default of 100 GB, and overridable per test so the
// refusal can be exercised without producing a file that size.
const defaultMaxUploadBytes = 1000 * 1000 * 1000

// uploadLimitMessage renders the refusal the way the API does. Its formatter
// always speaks in whole GB, so a ceiling below a billion bytes really does
// come back as "0 GB" — reproduced rather than tidied up.
func uploadLimitMessage(maxBytes int64) string {
	return fmt.Sprintf("Image exceeds the maximum upload size of %.0f GB", float64(maxBytes)/1000000000)
}

// allowedDiskFormats and allowedOsDistros are read from the platform's own
// configuration by the real API, not fixed in its code. They are fixed here
// only because a fake needs something to check against — the point being that
// the provider does no checking of its own and lets the API refuse.
var (
	// The values the platform actually accepts, copied from its own rejection
	// message. `img` is in the list and is not a format the storage layer
	// knows, which is why the service maps anything unrecognised to `detect`.
	allowedDiskFormats = map[string]bool{
		"iso": true, "aki": true, "ami": true, "ari": true, "img": true, "ploop": true,
		"qcow2": true, "raw": true, "vdi": true, "vhd": true, "vhdx": true, "vmdk": true,
	}
	// Abbreviated from the platform's list. The point is the shape: a
	// distribution carries its version, so a bare `ubuntu` is refused.
	allowedOsDistros = map[string]bool{
		"ubuntu20.04": true, "ubuntu18.04": true, "centos8": true, "centos7": true,
		"rockylinux8": true, "debian10": true, "win2k19": true, "windows": true,
	}
	allowedVisibility = map[string]bool{"public": true, "private": true, "shared": true, "community": true}
	// The four paths the update endpoint accepts. Everything else about an
	// image is fixed once it is created.
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
	// Bytes actually uploaded. Zero until the file arrives, which is what makes
	// a freshly created image report "0 MB".
	Bytes int64

	// Pending status change, counted down in details reads.
	pendingStatus string
	statusDelay   int

	// Pending field changes, keyed by patch path, each with its own countdown.
	pending map[string]*pendingValue

	deleted     bool
	deleteDelay int
}

// tick advances every pending change by one read. The caller reports the image
// before calling this, so a change with a delay of n is invisible for n reads.
//
// The counters advance together rather than in turn, so that waiting for one
// change cannot settle another — which is how a wait that watches the wrong
// thing gets covered by one that watches the right thing.
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

// detailsImage is a struct rather than a map on purpose: Go sorts map keys
// alphabetically, and a fixture that reorders fields hides the class of bug
// where a field that fails to decode takes everything declared after it with
// it. The field order here matches the API's.
//
// osType is `any` with omitempty because the details endpoint builds it from a
// value that is usually absent, and an absent value in JavaScript leaves the
// key out of the response rather than sending an empty string.
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

// createdImageBody is what create answers with: the storage layer's own image
// object, not the shape either read endpoint uses. A struct rather than a map
// so the field order is the API's — `id` arrives well down the response, which
// is the case the provider's id parser has to survive.
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

// listImage is the same image as the list endpoint reports it. Two differences
// from the details shape, both real: the distro is under a different key, and
// osType is filled in from the distro when the platform has none.
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

// notFound writes the body the images endpoints really send for a missing
// image, copied from the live API.
//
// Deliberately not the nested itemNotFound body the volume and snapshot fakes
// use. These routes hand the platform's own error straight back, and the
// platform answers with an **HTML page** — so `error` is a string, there is no
// numeric code anywhere in the body, and dterr.IsNotFound has nothing to read.
// What saves it is that the page carries its status in the title.
//
// That is the whole classification for this service, which is why it also has
// a test of its own rather than only being exercised through here.
func notFound(w http.ResponseWriter, id string) {
	acctest.WriteJSON(w, http.StatusNotFound, map[string]any{
		"error": fmt.Sprintf("<html>\n <head>\n  <title>404 Not Found</title>\n </head>\n"+
			" <body>\n  <h1>404 Not Found</h1>\n  No image found with ID %s<br /><br />\n\n\n\n"+
			" </body>\n</html>", id),
		"code": "SERVER_ERROR",
	})
}

// validationError reproduces the API's rejection body, including the leading
// space and trailing comma the real one carries: the middleware builds the
// string by reducing its validator's details.
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

// formatSize renders a byte count the way the API does: decimal GB with one
// decimal place above a billion bytes, whole MB below it. An image with no data
// therefore reads as "0 MB" rather than as nothing.
func formatSize(bytes int64) string {
	if bytes >= 1000000000 {
		return fmt.Sprintf("%.1f GB", float64(bytes)/1000000000)
	}
	return fmt.Sprintf("%.0f MB", float64(bytes)/1000000)
}

// formatMinVolumeSize renders min_disk the way the API does — as text, with a
// dash standing in for "none".
func formatMinVolumeSize(minDisk int) string {
	if minDisk == 0 {
		return "-"
	}
	return fmt.Sprintf("%d GB", minDisk)
}

// displayType is the category the API derives from the disk format. It is not
// the disk format, which is why the provider cannot round-trip that argument.
func displayType(diskFormat string) string {
	if diskFormat == "iso" {
		return "ISO"
	}
	return "Template (VM)"
}

// derivedOsType is the guess the list endpoint makes when the platform reports
// no os_type. The details endpoint makes no such guess.
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
//
// The rules it enforces are the API's, not the ones the provider would find
// convenient. In particular it reproduces:
//
//   - create opening an empty record that is `queued` and reports "0 MB",
//     with the data arriving in a second request, and answering with the
//     storage layer's own object rather than either read shape;
//   - the upload endpoint taking the image id and the declared size from the
//     query string, and refusing the request outright without the id;
//   - an upload whose byte count does not match the declared size being
//     rejected, and the image being deleted with it;
//   - the update endpoint taking one field per request, from a list of four;
//   - a patched value taking several reads to appear while the status stays
//     `active` throughout;
//   - the list and details endpoints disagreeing about the distro's key and
//     about os_type;
//   - every size being text, never a number;
//   - a missing image answering with the platform's own HTML error page wrapped
//     in a string, rather than the nested body the other services send.
type fakeImageAPI struct {
	mu     sync.Mutex
	images map[string]*fakeImage
	seq    int

	creates  int
	uploads  int
	updates  int
	deletes  int
	detailsN int

	// patchPaths records every path the provider patched, in order, so a test
	// can show that one request went out per changed field.
	patchPaths []string

	// readsAfterDelete counts reads of an image once its delete has been
	// accepted, and servedGone counts the 404s handed back once it has
	// actually gone. Together they say whether the provider polled the delete
	// through to the end or returned as soon as the request was accepted.
	readsAfterDelete int
	servedGone       int

	// uploadSawFileSize records whether the upload request declared a size.
	// The API tolerates its absence; the SDK is expected to send it, and
	// without it the platform cannot refuse an oversized file early.
	uploadSawFileSize bool

	// maxBytes is the upload ceiling this instance enforces. A test lowers it
	// to show the refusal without producing a file of that size.
	maxBytes int64

	// versions is the platform's own catalogue, which is a different list from
	// the images above and comes from a different table.
	versions map[string][]map[string]any
}

func newFakeImageAPI() *fakeImageAPI {
	return &fakeImageAPI{
		images:   map[string]*fakeImage{},
		maxBytes: defaultMaxUploadBytes,
		versions: map[string][]map[string]any{
			// The entry's own `type` is a display category, not an operating
			// system — the same two words the image endpoints report.
			"ubuntu": {
				// An array of flavor ids. Not a shape seen on the platform,
				// where every entry holds null; kept because the column is
				// untyped and a read must not fail on a value it did not write.
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

// seed adds an image the provider did not create, standing in for one that was
// already on the platform.
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

func (f *fakeImageAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !acctest.RequireAuth(w, r) {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	path := strings.TrimPrefix(r.URL.Path, "/openstack/images")
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
	case r.Method == http.MethodGet && len(seg) == 1 && seg[0] == "versions":
		acctest.WriteJSON(w, http.StatusOK, f.versions)
	case r.Method == http.MethodPost && len(seg) == 1 && seg[0] == "upload":
		f.upload(w, r)
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

func (f *fakeImageAPI) create(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "malformed body")
		return
	}

	name, _ := body["name"].(string)
	diskFormat, _ := body["type"].(string)
	osDistro, _ := body["os_distro"].(string)
	minDisk, _ := body["min_disk"].(float64)

	// Validation, in the order the API applies it.
	if name == "" {
		validationError(w, "'name' is required")
		return
	}
	if !noHTML.MatchString(name) {
		validationError(w, "Invalid characters in 'name'. HTML tags and special characters (<, >, &, ', \") are not allowed.")
		return
	}
	if !allowedDiskFormats[diskFormat] {
		validationError(w, "'type' must be one of [iso, aki, ami, ari, img, ploop, qcow2, raw, vdi, vhd, vhdx, vmdk]")
		return
	}
	if !allowedOsDistros[osDistro] {
		validationError(w, "'os_distro' must be one of [ubuntu20.04, ubuntu18.04, centos8, centos7, rockylinux8, debian10, win2k19, windows]")
		return
	}
	if minDisk < 1 || minDisk > 512 {
		validationError(w, "'min_disk' must be between 1 and 512")
		return
	}
	visibility, _ := body["visibility"].(string)
	if visibility == "" {
		// Omitting the field does not mean private: the service fills in
		// `shared`, and a provider whose default said otherwise would drift on
		// its first read.
		visibility = "shared"
	}
	if !allowedVisibility[visibility] {
		validationError(w, "'visibility' must be one of [public, private, shared, community]")
		return
	}

	// The guard the route applies before a single byte is sent. It can only
	// work if the caller declares the size here, which is why the provider
	// puts it in the create body as well as on the upload request.
	if declared, ok := body["fileSize"].(float64); ok && int64(declared) > f.maxBytes {
		acctest.WriteJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": uploadLimitMessage(f.maxBytes),
			"code":  "PAYLOAD_TOO_LARGE",
		})
		return
	}

	uefi, _ := body["uefi"].(bool)

	img := &fakeImage{
		ID:         f.nextID(),
		Name:       name,
		OsDistro:   osDistro,
		DiskFormat: diskFormat,
		MinDisk:    int(minDisk),
		Visibility: visibility,
		Uefi:       uefi,
		// The record exists and holds nothing. It stays this way until the file
		// arrives, and nothing can be built from it in the meantime.
		Status:  "queued",
		pending: map[string]*pendingValue{},
	}
	f.images[img.ID] = img
	f.creates++

	// The raw image object the storage layer produced, passed through without a
	// wrapper. Copied from the live API: 23 fields, with `id` sixteen fields in
	// rather than at the front, and the timestamps behind it.
	acctest.WriteJSON(w, http.StatusOK, createdImageBody{
		OsDistro:        img.OsDistro,
		Name:            img.Name,
		DiskFormat:      img.DiskFormat,
		ContainerFormat: "bare",
		Visibility:      img.Visibility,
		// No data yet, so both sizes and both hashes are null.
		Status:    img.Status,
		Protected: false,
		MinRAM:    0,
		MinDisk:   img.MinDisk,
		Owner:     "25dc6c29facb4ec5b3eb605ae85d2072",
		OsHidden:  false,
		ID:        img.ID,
		// Unlike every other timestamp in this API, these carry a timezone.
		// Nothing reads them, and they are here because the response has them.
		CreatedAt: "2026-07-08T10:35:17Z",
		UpdatedAt: "2026-07-08T10:35:17Z",
		// An array, never null — the field the platform validates as one.
		Tags:   []string{},
		Self:   "/v2/images/" + img.ID,
		File:   "/v2/images/" + img.ID + "/file",
		Schema: "/v2/schemas/image",
	})
}

// upload takes the file. The id and the declared size come from the query
// string; anything sent in the multipart body instead is ignored, which is
// exactly what makes a client that puts them there fail with a 400.
func (f *fakeImageAPI) upload(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("imageId")
	if id == "" {
		badRequest(w, "imageId query parameter is required")
		return
	}
	img := f.get(w, id)
	if img == nil {
		return
	}

	declared := int64(-1)
	if raw := r.URL.Query().Get("fileSize"); raw != "" {
		f.uploadSawFileSize = true
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			badRequest(w, "fileSize is not a number")
			return
		}
		declared = n
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		badRequest(w, "File not found")
		return
	}
	defer file.Close()

	// Counted from the stream rather than taken from a header, so a client that
	// understates the size cannot get past the check below.
	uploaded, err := io.Copy(io.Discard, file)
	if err != nil {
		badRequest(w, "read error")
		return
	}

	if uploaded > f.maxBytes {
		f.rejectUpload(w, img, http.StatusRequestEntityTooLarge, uploadLimitMessage(f.maxBytes))
		return
	}
	if declared >= 0 && uploaded != declared {
		// The API refuses a body that does not match the size the caller
		// promised, and takes the image down with it.
		f.rejectUpload(w, img, http.StatusBadRequest,
			"Uploaded file does not match the declared fileSize")
		return
	}

	f.uploads++
	img.Bytes = uploaded
	// The data is in; the platform still has work to do before the image can
	// be booted from.
	img.Status = "saving"
	img.pendingStatus = "active"
	img.statusDelay = settleReads

	acctest.WriteJSON(w, http.StatusOK, map[string]any{"msg": "Upload started"})
}

// rejectUpload refuses an upload and deletes the image with it, which is what
// the API does on every failed upload path. A provider that kept the id in
// state after one would be pointing at nothing.
func (f *fakeImageAPI) rejectUpload(w http.ResponseWriter, img *fakeImage, status int, message string) {
	img.deleted = true
	acctest.WriteJSON(w, status, map[string]any{"error": message, "code": "UPLOAD_REJECTED"})
}

func (f *fakeImageAPI) detailsView(img *fakeImage) detailsImage {
	return detailsImage{
		ID:     img.ID,
		Name:   img.Name,
		Status: img.Status,
		Size:   formatSize(img.Bytes),
		// No guess here: the details endpoint reports whatever the platform
		// holds, which for an uploaded image is nothing.
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
	// Reported before the tick, so a change with a delay of n really is
	// invisible for n reads.
	img.tick()
	acctest.WriteJSON(w, http.StatusOK, view)
}

func (f *fakeImageAPI) list(w http.ResponseWriter) {
	// The endpoint takes no filter worth having and no sort: everything visible
	// comes back in insertion order, and filtering is the provider's problem.
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

// update patches one field. The endpoint has no way to take two, which is why
// the provider sends a request per changed field.
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
		// The platform checks this, the update schema does not — it validates
		// only that a value is a string or a number.
		size, ok := body.Value.(float64)
		if !ok || size < 1 || size > 512 {
			validationError(w, "'min_disk' must be between 1 and 512")
			return
		}
	case "/os_distro":
		distro, _ := body.Value.(string)
		if !allowedOsDistros[distro] {
			validationError(w, "'os_distro' must be one of [ubuntu20.04, ubuntu18.04, centos8, centos7, rockylinux8, debian10, win2k19, windows]")
			return
		}
	case "/visibility":
		visibility, _ := body.Value.(string)
		if !allowedVisibility[visibility] {
			validationError(w, "'visibility' must be one of [public, private, shared, community]")
			return
		}
	}

	f.updates++
	f.patchPaths = append(f.patchPaths, body.Path)
	// The new value takes several reads to appear, and the status does not move
	// while it does. A waiter that watched the status would be satisfied at
	// once, having waited for nothing.
	img.pending[body.Path] = &pendingValue{value: body.Value, delay: valueSettleReads}

	// The response is the platform's raw object rather than the shape the read
	// endpoints use, so a provider that filled state from this body instead of
	// re-reading gets a different set of keys and notices.
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

// delete answers with a bare status code and no body, then lets the image
// linger for a few reads. The status does not change while it does: nothing
// but the image disappearing tells a caller the delete finished.
func (f *fakeImageAPI) delete(w http.ResponseWriter, id string) {
	img := f.get(w, id)
	if img == nil {
		return
	}
	f.deletes++
	img.deleteDelay = settleReads
	w.WriteHeader(http.StatusNoContent)
}
