package image_test

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// uploadBytes is the size of the file every test uploads. Chosen so the API's
// own formatting is exact rather than rounded: 3,000,000 bytes is "3 MB".
const uploadBytes = 3000000

// imageFile writes a file for the resource to upload and returns its path.
// The provider reads it during apply, so it has to be a real file rather than
// a name.
func imageFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(path, make([]byte, uploadBytes), 0o600); err != nil {
		t.Fatalf("writing the image file: %s", err)
	}
	return filepath.ToSlash(path)
}

// checkTerraformOwnedGone asserts that everything this provider created has
// been destroyed. Images seeded into the fake are skipped by name: they stand
// in for infrastructure Terraform does not own.
//
// This is also what pins the destroy waiter. The fake only advances a deleted
// image towards disappearing when it is read, so a Delete that returned without
// polling leaves the image alive here.
func checkTerraformOwnedGone(api *fakeImageAPI) func(*terraform.State) error {
	return func(*terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		for id, img := range api.images {
			if !strings.HasPrefix(img.Name, "tf-acc-") {
				continue
			}
			if !img.deleted {
				return fmt.Errorf("image %s (%s) still exists after destroy", id, img.Name)
			}
		}
		return nil
	}
}

// imageResource is the resource on its own. Used wherever a step removes the
// image underneath Terraform, since the data sources error on a missing image
// and would fail the step for the wrong reason.
func imageResource(endpoint, file, name, diskFormat, osDistro string, minDisk int, visibility string) string {
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(`
resource "dtcloud_image" "test" {
  name        = %q
  source_file = %q
  disk_format = %q
  os_distro   = %q
  min_disk    = %d
  visibility  = %q
}
`, name, file, diskFormat, osDistro, minDisk, visibility)
}

// imageConfig is the whole surface of the package in one configuration: the
// resource, both image data sources, and the catalogue.
func imageConfig(endpoint, file, name, diskFormat, osDistro string, minDisk int, visibility string) string {
	return imageResource(endpoint, file, name, diskFormat, osDistro, minDisk, visibility) + `
data "dtcloud_image" "by_id" {
  id = dtcloud_image.test.id
}

data "dtcloud_image" "by_name" {
  name       = dtcloud_image.test.name
  depends_on = [dtcloud_image.test]
}

data "dtcloud_images" "all" {
  depends_on = [dtcloud_image.test]
}

data "dtcloud_images" "shared" {
  visibility = "shared"
  depends_on = [dtcloud_image.test]
}

data "dtcloud_image_versions" "catalogue" {}
`
}

// TestAccDtcloudImage_lifecycle drives create → read → re-plan → import →
// destroy, with every data source reading along.
//
// Create is two requests: the endpoint opens an empty record and the file goes
// up separately. The size and status asserted here are what prove the second
// one happened — a record with no data reports "0 MB" and stays `queued`.
func TestAccDtcloudImage_lifecycle(t *testing.T) {
	api := newFakeImageAPI()
	server := httptest.NewServer(api)
	defer server.Close()
	file := imageFile(t)

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: imageConfig(server.URL, file, "tf-acc-image", "qcow2", "ubuntu20.04", 20, "shared"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_image.test", "name", "tf-acc-image"),
					resource.TestCheckResourceAttr("dtcloud_image.test", "os_distro", "ubuntu20.04"),
					resource.TestCheckResourceAttr("dtcloud_image.test", "min_disk", "20"),
					resource.TestCheckResourceAttr("dtcloud_image.test", "visibility", "shared"),
					resource.TestCheckResourceAttr("dtcloud_image.test", "uefi", "false"),
					resource.TestCheckResourceAttr("dtcloud_image.test", "status", "active"),
					// The size the file actually was, formatted by the platform.
					resource.TestCheckResourceAttr("dtcloud_image.test", "size", "3 MB"),
					// A display category derived from the disk format, not the
					// disk format itself.
					resource.TestCheckResourceAttr("dtcloud_image.test", "type", "Template (VM)"),
					// The details endpoint makes no guess at os_type, so the
					// resource has none.
					resource.TestCheckResourceAttr("dtcloud_image.test", "os_type", ""),
					resource.TestCheckResourceAttrSet("dtcloud_image.test", "id"),

					// The singular data source reads the same endpoint, by id
					// and by name alike.
					resource.TestCheckResourceAttrPair("data.dtcloud_image.by_id", "id", "dtcloud_image.test", "id"),
					resource.TestCheckResourceAttr("data.dtcloud_image.by_id", "min_disk", "20"),
					resource.TestCheckResourceAttr("data.dtcloud_image.by_id", "size", "3 MB"),
					resource.TestCheckResourceAttrPair("data.dtcloud_image.by_name", "id", "dtcloud_image.test", "id"),

					// The list endpoint fills os_type in from the distro where
					// the details endpoint left it empty. The same image,
					// described two ways — which is why both are exposed.
					resource.TestCheckResourceAttr("data.dtcloud_images.all", "images.#", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_images.all", "images.0.os_type", "linux"),
					resource.TestCheckResourceAttr("data.dtcloud_images.all", "images.0.os_distro", "ubuntu20.04"),
					resource.TestCheckResourceAttr("data.dtcloud_images.all", "images.0.min_disk", "20"),
					resource.TestCheckResourceAttr("data.dtcloud_images.shared", "images.#", "1"),
				),
			},
			{
				// Nothing changed, so nothing should be planned.
				Config:   imageConfig(server.URL, file, "tf-acc-image", "qcow2", "ubuntu20.04", 20, "shared"),
				PlanOnly: true,
			},
			{
				ResourceName:      "dtcloud_image.test",
				ImportState:       true,
				ImportStateVerify: true,
				// None of these is reported by any read endpoint, so an import
				// cannot recover them. Listing them here is the honest record
				// of what does not round-trip.
				ImportStateVerifyIgnore: []string{"source_file", "source_file_hash", "disk_format", "min_ram", "tags"},
			},
		},
	})
}

// TestAccDtcloudImage_createUploadsAndWaitsForActive is the rule that an image
// is not finished when the create call returns.
//
// The configuration has nothing to change afterwards, so no second wait can
// settle the status behind this one. Break the wait and the status asserted
// here is `saving`; skip the upload and it is `queued` with no data at all.
//
// It also covers where the upload's arguments go. The fake refuses an upload
// whose image id is not in the query string, which is where the API reads it
// from — so an SDK that puts it in the multipart body instead fails create
// here rather than in production.
func TestAccDtcloudImage_createUploadsAndWaitsForActive(t *testing.T) {
	api := newFakeImageAPI()
	server := httptest.NewServer(api)
	defer server.Close()
	file := imageFile(t)

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: imageResource(server.URL, file, "tf-acc-upload", "qcow2", "ubuntu20.04", 10, "private"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_image.test", "status", "active"),
					resource.TestCheckResourceAttr("dtcloud_image.test", "size", "3 MB"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.uploads != 1 {
							return fmt.Errorf("expected exactly one upload, got %d", api.uploads)
						}
						if !api.uploadSawFileSize {
							return fmt.Errorf("the upload did not declare a fileSize, so the platform " +
								"could not refuse an oversized file before receiving it")
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccDtcloudImage_createDeclaresTheFileSize is the rule that the platform
// is told how large the file is before it is sent.
//
// The create endpoint refuses an image that would exceed the upload ceiling,
// but it can only do so if the request says how big the file is. Without that
// the user uploads the whole thing and is refused at the end of it. The ceiling
// is lowered here rather than the file enlarged, so the refusal can be shown
// without writing a file that size.
func TestAccDtcloudImage_createDeclaresTheFileSize(t *testing.T) {
	api := newFakeImageAPI()
	api.maxBytes = 1000
	server := httptest.NewServer(api)
	defer server.Close()
	file := imageFile(t)

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config:      imageResource(server.URL, file, "tf-acc-toobig", "qcow2", "ubuntu20.04", 20, "shared"),
				ExpectError: regexp.MustCompile("exceeds the maximum upload size"),
			},
		},
	})

	api.mu.Lock()
	defer api.mu.Unlock()
	// The refusal has to come from create. The upload endpoint enforces the
	// same ceiling, so a provider that declared nothing would still be refused
	// — after sending the whole file, which is the thing being prevented.
	// Asserting on the error alone would pass either way.
	if api.creates != 0 {
		t.Fatalf("the image record was created (%d creates), so the refusal came from the upload "+
			"rather than from the declared size", api.creates)
	}
	if api.uploads != 0 {
		t.Fatalf("the file was uploaded despite the refusal (%d uploads)", api.uploads)
	}
}

// TestAccDtcloudImage_updateWaitsForEachValue covers the four fields the update
// endpoint accepts.
//
// Each one gets a step of its own. A single step changing all four would let a
// wait that checked only one of them pass, and would hide that the endpoint
// takes one field per request: the patch paths asserted at the end are the
// record of one request per changed field.
//
// The waits here cannot be on the status. An image stays `active` throughout an
// update, and the fake holds the old value for several reads while it does, so
// a status-only wait returns having waited for nothing.
func TestAccDtcloudImage_updateWaitsForEachValue(t *testing.T) {
	api := newFakeImageAPI()
	server := httptest.NewServer(api)
	defer server.Close()
	file := imageFile(t)

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: imageResource(server.URL, file, "tf-acc-update", "qcow2", "ubuntu20.04", 20, "shared"),
				Check:  resource.TestCheckResourceAttr("dtcloud_image.test", "name", "tf-acc-update"),
			},
			{
				Config: imageResource(server.URL, file, "tf-acc-update-renamed", "qcow2", "ubuntu20.04", 20, "shared"),
				Check:  resource.TestCheckResourceAttr("dtcloud_image.test", "name", "tf-acc-update-renamed"),
			},
			{
				// min_disk comes back inside "40 GB", so this step also covers
				// the parsing that makes the wait comparable at all.
				Config: imageResource(server.URL, file, "tf-acc-update-renamed", "qcow2", "ubuntu20.04", 40, "shared"),
				Check:  resource.TestCheckResourceAttr("dtcloud_image.test", "min_disk", "40"),
			},
			{
				Config: imageResource(server.URL, file, "tf-acc-update-renamed", "qcow2", "ubuntu20.04", 40, "private"),
				Check:  resource.TestCheckResourceAttr("dtcloud_image.test", "visibility", "private"),
			},
			{
				Config: imageResource(server.URL, file, "tf-acc-update-renamed", "qcow2", "centos8", 40, "private"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_image.test", "os_distro", "centos8"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						want := []string{"/name", "/min_disk", "/visibility", "/os_distro"}
						if !reflect.DeepEqual(api.patchPaths, want) {
							return fmt.Errorf("expected one request per changed field %v, got %v", want, api.patchPaths)
						}
						// The image was never rebuilt to apply any of them.
						if api.creates != 1 {
							return fmt.Errorf("expected the image to be updated in place, but it was created %d times", api.creates)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccDtcloudImage_diskFormatForcesNew is the rule that the file and its
// format are fixed once an image exists.
//
// There is no endpoint that replaces the data of an image, and the update
// endpoint does not accept the format, so the only way to honour a change is to
// build a new image.
func TestAccDtcloudImage_diskFormatForcesNew(t *testing.T) {
	api := newFakeImageAPI()
	server := httptest.NewServer(api)
	defer server.Close()
	file := imageFile(t)

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: imageResource(server.URL, file, "tf-acc-format", "qcow2", "ubuntu20.04", 20, "shared"),
				Check:  resource.TestCheckResourceAttr("dtcloud_image.test", "type", "Template (VM)"),
			},
			{
				Config: imageResource(server.URL, file, "tf-acc-format", "iso", "ubuntu20.04", 20, "shared"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_image.test", "type", "ISO"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.creates != 2 {
							return fmt.Errorf("expected the image to be rebuilt, but it was created %d times", api.creates)
						}
						if api.updates != 0 {
							return fmt.Errorf("the disk format was patched (%d updates); the endpoint does not accept it", api.updates)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccDtcloudImage_destroyWaitsForTheImageToBeGone is the rule that destroy
// does not return while the platform is still reclaiming the image.
//
// The fake answers a delete with a bare status and then keeps reporting the
// image for several reads, without changing its status — nothing but the image
// disappearing says the delete finished.
//
// Written first with CheckDestroy as the only assertion, which turned out to
// prove nothing: breaking the waiter so that it returned immediately left the
// test green, because by the time CheckDestroy runs the fake's countdown has
// drained regardless. The assertion that bites is inside the step, and it is on
// whether the provider ever received a 404 — the one thing a Delete that
// stopped looking cannot have seen.
func TestAccDtcloudImage_destroyWaitsForTheImageToBeGone(t *testing.T) {
	api := newFakeImageAPI()
	server := httptest.NewServer(api)
	defer server.Close()
	file := imageFile(t)

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: imageResource(server.URL, file, "tf-acc-destroy", "qcow2", "ubuntu20.04", 20, "shared"),
			},
			{
				Config:  imageResource(server.URL, file, "tf-acc-destroy", "qcow2", "ubuntu20.04", 20, "shared"),
				Destroy: true,
				Check: func(*terraform.State) error {
					api.mu.Lock()
					defer api.mu.Unlock()
					// The assertion is that the provider *saw* the image go,
					// not merely that it is gone by the time anything checks.
					// CheckDestroy alone cannot tell the difference: it runs
					// late enough that the fake has drained its countdown
					// anyway, so a Delete that returned the moment the request
					// was accepted passes it. This does not.
					if api.servedGone == 0 {
						return fmt.Errorf("destroy returned without ever seeing the image gone: the "+
							"delete was acknowledged and the provider stopped looking after %d read(s)",
							api.readsAfterDelete)
					}
					return nil
				},
			},
		},
	})
}

// TestAccDtcloudImage_deletedOutsideTerraformIsDroppedFromState is the rule
// that a missing image is dropped from state rather than failing the run.
//
// It matters more here than elsewhere. These endpoints hand the platform's own
// error body back rather than the nested one the other services send, and the
// only numeric status in it is gone by the time the SDK sees it — so the
// classification rests entirely on matching the message text. This test is what
// says that path still works for images.
func TestAccDtcloudImage_deletedOutsideTerraformIsDroppedFromState(t *testing.T) {
	api := newFakeImageAPI()
	server := httptest.NewServer(api)
	defer server.Close()
	file := imageFile(t)
	config := imageResource(server.URL, file, "tf-acc-vanished", "qcow2", "ubuntu20.04", 20, "shared")

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				// Somebody deleted it in the console. A refresh step carries
				// the previous step's configuration, so it cannot repeat it.
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()
					for _, img := range api.images {
						img.deleted = true
					}
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccDtcloudImage_ambiguousNameLookupFails is the rule that a name is not
// an identifier on this platform.
//
// Two images may carry the same name, and picking the first match would build
// machines from an image nobody chose.
func TestAccDtcloudImage_ambiguousNameLookupFails(t *testing.T) {
	api := newFakeImageAPI()
	api.seed(&fakeImage{Name: "shared-name", OsDistro: "ubuntu20.04", DiskFormat: "qcow2", MinDisk: 10, Visibility: "public", Status: "active", Bytes: 1000000})
	api.seed(&fakeImage{Name: "shared-name", OsDistro: "centos8", DiskFormat: "qcow2", MinDisk: 10, Visibility: "public", Status: "active", Bytes: 1000000})
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
data "dtcloud_image" "ambiguous" {
  name = "shared-name"
}
`,
				ExpectError: regexp.MustCompile(`2 images are named "shared-name"`),
			},
		},
	})
}

// TestAccDtcloudImage_versionsFlattensEveryFlavorShape covers the catalogue.
//
// The flavor list is stored by the platform rather than produced by OpenStack,
// and the same column holds a JSON array, a comma-separated string and null.
// A data source that handled only one of them would fail a read for an entry
// somebody else wrote.
func TestAccDtcloudImage_versionsFlattensEveryFlavorShape(t *testing.T) {
	api := newFakeImageAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + `
data "dtcloud_image_versions" "all" {}

data "dtcloud_image_versions" "ubuntu" {
  type = "ubuntu"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					// Sorted by family and then by version, so the order does
					// not move when the catalogue gains an entry.
					resource.TestCheckResourceAttr("data.dtcloud_image_versions.all", "versions.#", "3"),
					resource.TestCheckResourceAttr("data.dtcloud_image_versions.all", "versions.0.type", "ubuntu"),
					resource.TestCheckResourceAttr("data.dtcloud_image_versions.all", "versions.0.version", "20.04"),
					resource.TestCheckResourceAttr("data.dtcloud_image_versions.all", "versions.0.image_type", "Template (VM)"),
					// A number here, unlike every size on the image endpoints.
					resource.TestCheckResourceAttr("data.dtcloud_image_versions.all", "versions.0.min_volume_size", "20"),
					// An array of ids.
					resource.TestCheckResourceAttr("data.dtcloud_image_versions.all", "versions.0.valid_flavor_ids.#", "2"),
					resource.TestCheckResourceAttr("data.dtcloud_image_versions.all", "versions.0.valid_flavor_ids.0", "flavor-1"),
					// Null: no restriction, not an error.
					resource.TestCheckResourceAttr("data.dtcloud_image_versions.all", "versions.1.valid_flavor_ids.#", "0"),
					// A comma-separated string, split and trimmed.
					resource.TestCheckResourceAttr("data.dtcloud_image_versions.all", "versions.2.type", "windows"),
					resource.TestCheckResourceAttr("data.dtcloud_image_versions.all", "versions.2.valid_flavor_ids.#", "2"),
					resource.TestCheckResourceAttr("data.dtcloud_image_versions.all", "versions.2.valid_flavor_ids.1", "flavor-4"),

					resource.TestCheckResourceAttr("data.dtcloud_image_versions.ubuntu", "versions.#", "2"),
				),
			},
		},
	})
}
