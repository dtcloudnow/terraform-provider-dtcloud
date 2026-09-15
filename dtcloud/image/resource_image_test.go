package image_test

import (
	"fmt"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// sourceVolume seeds an available volume for the capture to be pointed at, and
// returns its id. 20 GB, because min_disk is inherited from the volume's size.
func sourceVolume(api *fakeImageAPI) string {
	return api.seedVolume(&fakeVolume{Name: "tf-acc-source", Size: 20}).ID
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
func imageResource(endpoint, volumeID, name, diskFormat, osDistro string, minDisk int, visibility string) string {
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(`
resource "dtcloud_image" "test" {
  name             = %q
  source_volume_id = %q
  disk_format      = %q
  os_distro        = %q
  min_disk         = %d
  visibility       = %q
}
`, name, volumeID, diskFormat, osDistro, minDisk, visibility)
}

// imageInherited is the same resource with os_distro and min_disk left out, so
// the capture's inherited values are what land in state.
func imageInherited(endpoint, volumeID, name, diskFormat string) string {
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(`
resource "dtcloud_image" "test" {
  name             = %q
  source_volume_id = %q
  disk_format      = %q
}
`, name, volumeID, diskFormat)
}

// imageConfig is the whole surface of the package in one configuration.
func imageConfig(endpoint, volumeID, name, diskFormat, osDistro string, minDisk int, visibility string) string {
	return imageResource(endpoint, volumeID, name, diskFormat, osDistro, minDisk, visibility) + `
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
	volume := sourceVolume(api)

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: imageConfig(server.URL, volume, "tf-acc-image", "qcow2", "debian12", 20, "shared"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_image.test", "name", "tf-acc-image"),
					resource.TestCheckResourceAttr("dtcloud_image.test", "os_distro", "debian12"),
					resource.TestCheckResourceAttr("dtcloud_image.test", "min_disk", "20"),
					resource.TestCheckResourceAttr("dtcloud_image.test", "visibility", "shared"),
					resource.TestCheckResourceAttr("dtcloud_image.test", "uefi", "false"),
					resource.TestCheckResourceAttr("dtcloud_image.test", "status", "active"),
					// The size the file actually was, formatted by the platform.
					resource.TestCheckResourceAttr("dtcloud_image.test", "size", "3 MB"),
					// A display category derived from the disk format, not the format.
					resource.TestCheckResourceAttr("dtcloud_image.test", "type", "Template (VM)"),
					// A captured image carries os_type; the platform works it out from
					// the volume rather than leaving it blank as an upload did.
					resource.TestCheckResourceAttr("dtcloud_image.test", "os_type", "linux"),
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
					resource.TestCheckResourceAttr("data.dtcloud_images.all", "images.0.os_distro", "debian12"),
					resource.TestCheckResourceAttr("data.dtcloud_images.all", "images.0.min_disk", "20"),
					resource.TestCheckResourceAttr("data.dtcloud_images.shared", "images.#", "1"),
				),
			},
			{
				// Nothing changed, so nothing should be planned.
				Config:   imageConfig(server.URL, volume, "tf-acc-image", "qcow2", "debian12", 20, "shared"),
				PlanOnly: true,
			},
			{
				ResourceName:      "dtcloud_image.test",
				ImportState:       true,
				ImportStateVerify: true,
				// Reported by no read endpoint, so an import cannot recover them.
				// All three are ForceNew, which is why a real import needs a
				// lifecycle{ignore_changes} block - see docs/resources/image.md.
				ImportStateVerifyIgnore: []string{"source_volume_id", "disk_format", "container_format"},
			},
		},
	})
}

// TestAccDtcloudImage_updateWaitsForEachValue covers the four fields update
// accepts, one step each — a single step changing all four would let a wait
// that checked only one of them pass. The waits cannot be on the status: an
// image stays `active` throughout while the old value is still reported.
func TestAccDtcloudImage_updateWaitsForEachValue(t *testing.T) {
	api := newFakeImageAPI()
	server := httptest.NewServer(api)
	defer server.Close()
	volume := sourceVolume(api)

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: imageResource(server.URL, volume, "tf-acc-update", "qcow2", "debian12", 20, "shared"),
				Check:  resource.TestCheckResourceAttr("dtcloud_image.test", "name", "tf-acc-update"),
			},
			{
				Config: imageResource(server.URL, volume, "tf-acc-update-renamed", "qcow2", "debian12", 20, "shared"),
				Check:  resource.TestCheckResourceAttr("dtcloud_image.test", "name", "tf-acc-update-renamed"),
			},
			{
				// min_disk comes back inside "40 GB", so this also covers the parsing.
				Config: imageResource(server.URL, volume, "tf-acc-update-renamed", "qcow2", "debian12", 40, "shared"),
				Check:  resource.TestCheckResourceAttr("dtcloud_image.test", "min_disk", "40"),
			},
			{
				Config: imageResource(server.URL, volume, "tf-acc-update-renamed", "qcow2", "debian12", 40, "private"),
				Check:  resource.TestCheckResourceAttr("dtcloud_image.test", "visibility", "private"),
			},
			{
				Config: imageResource(server.URL, volume, "tf-acc-update-renamed", "qcow2", "centos8", 40, "private"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_image.test", "os_distro", "centos8"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						// The first two come from CREATE, not from an update: the
						// capture cannot take os_distro or min_disk, so the provider
						// patches whatever the configuration asked for right after it.
						// The remaining four are one request per changed field.
						want := []string{
							"/os_distro", "/min_disk",
							"/name", "/min_disk", "/visibility", "/os_distro",
						}
						if !reflect.DeepEqual(api.patchPaths, want) {
							return fmt.Errorf("expected one request per changed field %v, got %v", want, api.patchPaths)
						}
						// The image was never rebuilt to apply any of them.
						if api.captures != 1 {
							return fmt.Errorf("expected the image to be updated in place, but it was captured %d times", api.captures)
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
	volume := sourceVolume(api)

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				// os_distro and min_disk are left out on purpose: inheriting them
				// means the create patches nothing, so the update count below
				// measures only what a format change did.
				Config: imageInherited(server.URL, volume, "tf-acc-format", "qcow2"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_image.test", "type", "Template (VM)"),
					// Inherited from the volume, not from the configuration.
					resource.TestCheckResourceAttr("dtcloud_image.test", "os_distro", "debian12"),
					resource.TestCheckResourceAttr("dtcloud_image.test", "min_disk", "20"),
				),
			},
			{
				Config: imageInherited(server.URL, volume, "tf-acc-format", "raw"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_image.test", "disk_format", "raw"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.captures != 2 {
							return fmt.Errorf("expected the image to be rebuilt, but it was captured %d times", api.captures)
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
	volume := sourceVolume(api)

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: imageResource(server.URL, volume, "tf-acc-destroy", "qcow2", "debian12", 20, "shared"),
			},
			{
				Config:  imageResource(server.URL, volume, "tf-acc-destroy", "qcow2", "debian12", 20, "shared"),
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
	volume := sourceVolume(api)
	config := imageResource(server.URL, volume, "tf-acc-vanished", "qcow2", "debian12", 20, "shared")

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
	api.seed(&fakeImage{Name: "shared-name", OsDistro: "debian12", DiskFormat: "qcow2", MinDisk: 10, Visibility: "public", Status: "active", Bytes: 1000000})
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
