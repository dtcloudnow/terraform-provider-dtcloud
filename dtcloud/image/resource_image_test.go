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

// uploadBytes is the size every test uploads: 3,000,000 bytes formats as "3 MB"
// exactly, so nothing is rounded.
const uploadBytes = 3000000

// imageFile writes a file for the resource to upload and returns its path.
func imageFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(path, make([]byte, uploadBytes), 0o600); err != nil {
		t.Fatalf("writing the image file: %s", err)
	}
	return filepath.ToSlash(path)
}

// checkTerraformOwnedGone asserts everything this provider created is gone.
// Seeded images are skipped by name. It also pins the destroy waiter: the fake
// only advances a deleted image towards disappearing when it is read.
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

// imageResource is the resource on its own, for steps that remove the image
// underneath Terraform — the data sources would fail for the wrong reason.
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

// imageConfig is the whole surface of the package in one configuration.
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
// destroy. Create is two requests, and the size and status asserted here are
// what prove the second one happened.
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
					// A display category derived from the disk format, not the format.
					resource.TestCheckResourceAttr("dtcloud_image.test", "type", "Template (VM)"),
					// The details endpoint makes no guess at os_type.
					resource.TestCheckResourceAttr("dtcloud_image.test", "os_type", ""),
					resource.TestCheckResourceAttrSet("dtcloud_image.test", "id"),

					// The singular data source reads the same endpoint, by id and by name.
					resource.TestCheckResourceAttrPair("data.dtcloud_image.by_id", "id", "dtcloud_image.test", "id"),
					resource.TestCheckResourceAttr("data.dtcloud_image.by_id", "min_disk", "20"),
					resource.TestCheckResourceAttr("data.dtcloud_image.by_id", "size", "3 MB"),
					resource.TestCheckResourceAttrPair("data.dtcloud_image.by_name", "id", "dtcloud_image.test", "id"),

					// The list endpoint fills os_type in where details left it empty — the
					// same image described two ways, which is why both are exposed.
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
				// Reported by no read endpoint, so an import cannot recover them.
				ImportStateVerifyIgnore: []string{"source_file", "source_file_hash", "disk_format", "min_ram", "tags"},
			},
		},
	})
}

// TestAccDtcloudImage_createUploadsAndWaitsForActive is the rule that an image
// is not finished when the create call returns. Nothing changes afterwards, so
// no second wait can settle the status behind this one. It also covers where
// the upload's arguments go: the id belongs in the query string.
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

// TestAccDtcloudImage_createDeclaresTheFileSize is the rule that the platform is
// told the file size before it is sent, so an oversized upload is refused up
// front. The ceiling is lowered rather than the file enlarged.
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
	// The refusal has to come from create: upload enforces the same ceiling, so
	// asserting on the error alone would pass either way.
	if api.creates != 0 {
		t.Fatalf("the image record was created (%d creates), so the refusal came from the upload "+
			"rather than from the declared size", api.creates)
	}
	if api.uploads != 0 {
		t.Fatalf("the file was uploaded despite the refusal (%d uploads)", api.uploads)
	}
}

// TestAccDtcloudImage_updateWaitsForEachValue covers the four fields update
// accepts, one step each — a single step changing all four would let a wait
// that checked only one of them pass. The waits cannot be on the status: an
// image stays `active` throughout while the old value is still reported.
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
				// min_disk comes back inside "40 GB", so this also covers the parsing.
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
// format are fixed once an image exists: nothing replaces an image's data, so
// the only way to honour a change is to build a new one.
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
// does not return while the platform is still reclaiming the image. Nothing but
// the image disappearing says the delete finished, so the assertion is on
// whether the provider ever received a 404.
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
					// The assertion is that the provider saw the image go, not that it is gone by
					// the time anything checks. CheckDestroy runs late enough to pass either way.
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

// TestAccDtcloudImage_deletedOutsideTerraformIsDroppedFromState is the rule that
// a missing image is dropped from state rather than failing the run. These
// routes carry no numeric status by the time the SDK sees them, so the
// classification rests on matching the message text.
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
				// Somebody deleted it in the console. A refresh step carries the previous
				// step's configuration, so it cannot repeat it.
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

// TestAccDtcloudImage_ambiguousNameLookupFails is the rule that a name is not an
// identifier: two images may share one, and picking the first match would build
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

// TestAccDtcloudImage_versionsFlattensEveryFlavorShape covers the catalogue,
// whose flavor column holds a JSON array, a comma-separated string or null. A
// data source handling one shape would fail on an entry somebody else wrote.
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
					// Sorted by family then version, so the order does not move.
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
