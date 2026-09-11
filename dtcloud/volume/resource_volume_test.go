package volume_test

import (
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// checkTerraformOwnedGone asserts everything this provider created is gone.
// Seeded volumes are skipped by name — Terraform does not own them.
func checkTerraformOwnedGone(api *fakeVolumeAPI) func(*terraform.State) error {
	return func(*terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		for id, v := range api.volumes {
			if !strings.HasPrefix(v.Name, "tf-acc-") {
				continue
			}
			if !v.deleted {
				return fmt.Errorf("volume %s (%s) still exists after destroy", id, v.Name)
			}
		}
		return nil
	}
}

func volumeConfig(endpoint, name, policy, description string, size int) string {
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(`
resource "dtcloud_volume" "test" {
  name           = %q
  size           = %d
  storage_policy = %q
  description    = %q
}

data "dtcloud_volume" "test" {
  id = dtcloud_volume.test.id
}

data "dtcloud_volumes" "all" {
  depends_on = [dtcloud_volume.test]
}

data "dtcloud_storage_policies" "all" {}
`, name, size, policy, description)
}

// TestAccDtcloudVolume_lifecycle drives create → read → re-plan → in-place
// rename, grow and retype → import → destroy.
func TestAccDtcloudVolume_lifecycle(t *testing.T) {
	api := newFakeVolumeAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: volumeConfig(server.URL, "tf-acc-vol", "standard", "first", 20),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_volume.test", "name", "tf-acc-vol"),
					resource.TestCheckResourceAttr("dtcloud_volume.test", "size", "20"),
					resource.TestCheckResourceAttr("dtcloud_volume.test", "storage_policy", "standard"),
					resource.TestCheckResourceAttr("dtcloud_volume.test", "status", "available"),
					// "false" arrives as a string and has to become a bool.
					resource.TestCheckResourceAttr("dtcloud_volume.test", "bootable", "false"),
					resource.TestCheckResourceAttr("dtcloud_volume.test", "volume_type", "HDD"),
					resource.TestCheckResourceAttr("dtcloud_volume.test", "attached_to_id", ""),
					// false here, and true from the list route below for the same volume.
					resource.TestCheckResourceAttr("dtcloud_volume.test", "is_detachable", "false"),
					resource.TestCheckResourceAttr("dtcloud_volume.test", "created", acctest.FakeCreatedAt),
					// A blank volume carries no image metadata at all.
					resource.TestCheckResourceAttr("dtcloud_volume.test", "image_metadata.#", "0"),
					// The id came back as `volumeId`, not `id`.
					resource.TestCheckResourceAttrSet("dtcloud_volume.test", "id"),

					resource.TestCheckResourceAttr("data.dtcloud_volume.test", "name", "tf-acc-vol"),
					resource.TestCheckResourceAttr("data.dtcloud_volume.test", "size", "20"),
					resource.TestCheckResourceAttr("data.dtcloud_volume.test", "bootable", "false"),

					// The list endpoint reports "20 GB"; the provider parses it.
					resource.TestCheckResourceAttr("data.dtcloud_volumes.all", "volumes.#", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_volumes.all", "volumes.0.size", "20"),
					resource.TestCheckResourceAttr("data.dtcloud_volumes.all", "volumes.0.name", "tf-acc-vol"),
					resource.TestCheckResourceAttr("data.dtcloud_volumes.all", "volumes.0.bootable", "false"),
					// The other half of the contradiction pinned above.
					resource.TestCheckResourceAttr("data.dtcloud_volumes.all", "volumes.0.is_detachable", "true"),

					resource.TestCheckResourceAttr("data.dtcloud_storage_policies.all", "policies.#", "2"),
					resource.TestCheckResourceAttr("data.dtcloud_storage_policies.all", "policies.0.name", "standard"),
					resource.TestCheckResourceAttr("data.dtcloud_storage_policies.all", "names.1", "fast"),

					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						// Applied as a follow-up update: create accepts no description.
						if api.updates != 1 {
							return fmt.Errorf("expected one update call to set the description, saw %d", api.updates)
						}
						return nil
					},
				),
			},
			{
				// Re-applying must be a no-op, `description` included — it is never reported
				// back and must not look like a change on every plan.
				Config:   volumeConfig(server.URL, "tf-acc-vol", "standard", "first", 20),
				PlanOnly: true,
			},
			{
				// Rename and re-describe. Both go through the update route.
				Config: volumeConfig(server.URL, "tf-acc-vol-renamed", "standard", "second", 20),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_volume.test", "name", "tf-acc-vol-renamed"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.creates != 1 {
							return fmt.Errorf("in-place updates must not recreate the volume; %d creates seen", api.creates)
						}
						return nil
					},
				),
			},
			{
				// Grow, and nothing else. On its own deliberately: changing the size and the
				// policy together lets the retype wait hide a broken size wait.
				Config: volumeConfig(server.URL, "tf-acc-vol-renamed", "standard", "second", 40),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_volume.test", "size", "40"),
					resource.TestCheckResourceAttr("dtcloud_volume.test", "storage_policy", "standard"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.creates != 1 {
							return fmt.Errorf("growing must not recreate the volume; %d creates seen", api.creates)
						}
						if api.extends != 1 {
							return fmt.Errorf("expected exactly one osExtend, saw %d", api.extends)
						}
						return nil
					},
				),
			},
			{
				// Retype, and nothing else.
				Config: volumeConfig(server.URL, "tf-acc-vol-renamed", "fast", "second", 40),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_volume.test", "storage_policy", "fast"),
					resource.TestCheckResourceAttr("dtcloud_volume.test", "size", "40"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.creates != 1 {
							return fmt.Errorf("retyping must not recreate the volume; %d creates seen", api.creates)
						}
						if api.retypes != 1 {
							return fmt.Errorf("expected exactly one osRetype, saw %d", api.retypes)
						}
						return nil
					},
				),
			},
			{
				Config:   volumeConfig(server.URL, "tf-acc-vol-renamed", "fast", "second", 40),
				PlanOnly: true,
			},
			{
				ResourceName:      "dtcloud_volume.test",
				ImportState:       true,
				ImportStateVerify: true,
				// The details endpoint leaves the description out, so there is nothing to
				// import it from. Documented on the resource page rather than papered over.
				ImportStateVerifyIgnore: []string{"description"},
			},
		},
	})
}

// TestAccDtcloudVolume_fromImage covers the bootable path: a different create
// status, a string "true" to turn into a bool, and the image metadata block.
func TestAccDtcloudVolume_fromImage(t *testing.T) {
	api := newFakeVolumeAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	config := acctest.ProviderConfig(server.URL) + `
resource "dtcloud_volume" "boot" {
  name           = "tf-acc-vol-boot"
  size           = 30
  storage_policy = "standard"
  image_id       = "img-1234"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_volume.boot", "bootable", "true"),
					resource.TestCheckResourceAttr("dtcloud_volume.boot", "image_id", "img-1234"),
					resource.TestCheckResourceAttr("dtcloud_volume.boot", "image_metadata.#", "1"),
					resource.TestCheckResourceAttr("dtcloud_volume.boot", "image_metadata.0.image_id", "img-1234"),
					resource.TestCheckResourceAttr("dtcloud_volume.boot", "image_metadata.0.disk_format", "qcow2"),
					resource.TestCheckResourceAttr("dtcloud_volume.boot", "image_metadata.0.os_distro", "ubuntu"),
					// Reported as a string; kept as one.
					resource.TestCheckResourceAttr("dtcloud_volume.boot", "image_metadata.0.min_ram", "512"),
					resource.TestCheckResourceAttr("dtcloud_volume.boot", "status", "available"),
				),
			},
			{
				// Never echoed back, so what keeps it stable is that Read leaves it alone.
				Config:   config,
				PlanOnly: true,
			},
			{
				ResourceName:      "dtcloud_volume.boot",
				ImportState:       true,
				ImportStateVerify: true,
				// Neither is reported by the details endpoint.
				ImportStateVerifyIgnore: []string{"description", "image_id"},
			},
		},
	})
}

// TestAccDtcloudVolume_clone pins the second create shape: clone answers with
// the raw volume keyed `id` where create answers with `volumeId`.
func TestAccDtcloudVolume_clone(t *testing.T) {
	api := newFakeVolumeAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	source := api.seed("seed-source", "standard", 25)

	config := acctest.ProviderConfig(server.URL) + fmt.Sprintf(`
resource "dtcloud_volume" "copy" {
  name             = "tf-acc-vol-clone"
  size             = 25
  storage_policy   = "standard"
  source_volume_id = %q
}
`, source.ID)

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_volume.copy", "name", "tf-acc-vol-clone"),
					resource.TestCheckResourceAttr("dtcloud_volume.copy", "size", "25"),
					resource.TestCheckResourceAttrSet("dtcloud_volume.copy", "id"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.clones != 1 {
							return fmt.Errorf("expected the clone route to be used once, saw %d", api.clones)
						}
						if api.creates != 0 {
							return fmt.Errorf("source_volume_id must not go through the create route; %d creates seen", api.creates)
						}
						return nil
					},
				),
			},
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

// The VM a volume gets attached to in TestAccDtcloudVolume_attached. It exists
// only as the identity the details endpoint reports on an attached volume.
const (
	attachedVMName   = "tf-acc-web-01"
	attachedVMID     = "vm-9f1c2b3a"
	attachedVMStatus = "ACTIVE"
)

func attachedVolumeConfig(endpoint, name, policy string, size int) string {
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(`
resource "dtcloud_volume" "attached" {
  name           = %q
  size           = %d
  storage_policy = %q

  # Deliberately short. This test turns on the waiter accepting in-use as a
  # resting state; if it stopped doing so the wait would never settle, and the
  # 30-minute default would turn a clear failure into a stalled test run.
  timeouts {
    update = "2m"
  }
}

data "dtcloud_volume" "attached" {
  id = dtcloud_volume.attached.id
}

data "dtcloud_volumes" "on_vm" {
  attached_to_id = %q

  depends_on = [dtcloud_volume.attached]
}
`, name, size, policy, attachedVMID)
}

// TestAccDtcloudVolume_attached covers the half of this package that only
// applies to an attached volume, including volumeSettled treating `in-use` as a
// resting state — growing a disk in use is the ordinary production case, and it
// never passes through `available`. No VM is created; see attachTo().
func TestAccDtcloudVolume_attached(t *testing.T) {
	api := newFakeVolumeAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	const name = "tf-acc-vol-attached"

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				// Detached, as everything else in this package is.
				Config: attachedVolumeConfig(server.URL, name, "standard", 20),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_volume.attached", "status", "available"),
					resource.TestCheckResourceAttr("dtcloud_volume.attached", "attached_to", ""),
					resource.TestCheckResourceAttr("dtcloud_volume.attached", "attached_to_id", ""),
					resource.TestCheckResourceAttr("dtcloud_volume.attached", "is_detachable", "false"),
					// Nothing to match yet, which is the half that proves it filters at all.
					resource.TestCheckResourceAttr("data.dtcloud_volumes.on_vm", "volumes.#", "0"),
				),
			},
			{
				// Attached out of band; this package only ever sees the result.
				PreConfig: func() { api.attachTo(name, attachedVMName, attachedVMID, attachedVMStatus) },
				Config:    attachedVolumeConfig(server.URL, name, "standard", 20),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_volume.attached", "status", "in-use"),
					resource.TestCheckResourceAttr("dtcloud_volume.attached", "attached_to", attachedVMName),
					resource.TestCheckResourceAttr("dtcloud_volume.attached", "attached_to_id", attachedVMID),
					resource.TestCheckResourceAttr("dtcloud_volume.attached", "attached_to_status", attachedVMStatus),
					resource.TestCheckResourceAttr("dtcloud_volume.attached", "is_detachable", "true"),

					resource.TestCheckResourceAttr("data.dtcloud_volume.attached", "attached_to", attachedVMName),
					resource.TestCheckResourceAttr("data.dtcloud_volume.attached", "is_detachable", "true"),

					// Now it matches. The two routes disagree above and agree here.
					resource.TestCheckResourceAttr("data.dtcloud_volumes.on_vm", "volumes.#", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_volumes.on_vm", "volumes.0.name", name),
					resource.TestCheckResourceAttr("data.dtcloud_volumes.on_vm", "volumes.0.status", "in-use"),
					resource.TestCheckResourceAttr("data.dtcloud_volumes.on_vm", "volumes.0.attached_to", attachedVMName),
				),
			},
			{
				// The step this test exists for: grow a volume that is in use. The status
				// never leaves `in-use`, so the wait ends only once the size lands.
				Config: attachedVolumeConfig(server.URL, name, "standard", 40),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_volume.attached", "size", "40"),
					resource.TestCheckResourceAttr("dtcloud_volume.attached", "status", "in-use"),
					resource.TestCheckResourceAttr("dtcloud_volume.attached", "attached_to_id", attachedVMID),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.extends != 1 {
							return fmt.Errorf("expected exactly one osExtend, saw %d", api.extends)
						}
						if api.creates != 1 {
							return fmt.Errorf("growing an attached volume must not recreate it; %d creates seen", api.creates)
						}
						return nil
					},
				),
			},
			{
				// Destroying while attached must be refused, and must not detach on its own:
				// pulling a disk out of a running machine to satisfy a plan is the same class
				// of silent damage as shrinking a volume.
				Destroy:     true,
				Config:      attachedVolumeConfig(server.URL, name, "standard", 40),
				ExpectError: regexp.MustCompile(`still attached to .*detach it first`),
			},
			{
				// Detached, which is the supported way round.
				PreConfig: func() { api.detach(name) },
				Config:    attachedVolumeConfig(server.URL, name, "standard", 40),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_volume.attached", "status", "available"),
					resource.TestCheckResourceAttr("dtcloud_volume.attached", "attached_to", ""),
					resource.TestCheckResourceAttr("dtcloud_volume.attached", "is_detachable", "false"),
					resource.TestCheckResourceAttr("data.dtcloud_volumes.on_vm", "volumes.#", "0"),
				),
			},
		},
	})
}

// TestAccDtcloudVolume_snapshots reads the snapshots of a volume Terraform does
// not own: seeing what a cascade delete would take with it.
func TestAccDtcloudVolume_snapshots(t *testing.T) {
	api := newFakeVolumeAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	source := api.seed("seed-snapshotted", "standard", 50,
		fakeSnapshot{ID: "snap-1", Name: "nightly", Status: "available", Description: "cron", Size: 50, StoragePolicy: "standard"},
		fakeSnapshot{ID: "snap-2", Name: "manual", Status: "available", Description: "", Size: 50, StoragePolicy: "standard"},
	)

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig(server.URL) + fmt.Sprintf(`
data "dtcloud_volume_snapshots" "seen" {
  volume_id = %q
}
`, source.ID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.dtcloud_volume_snapshots.seen", "snapshots.#", "2"),
					resource.TestCheckResourceAttr("data.dtcloud_volume_snapshots.seen", "snapshots.0.name", "nightly"),
					resource.TestCheckResourceAttr("data.dtcloud_volume_snapshots.seen", "snapshots.0.size", "50"),
					resource.TestCheckResourceAttr("data.dtcloud_volume_snapshots.seen", "snapshots.0.storage_policy", "standard"),
					resource.TestCheckResourceAttr("data.dtcloud_volume_snapshots.seen", "snapshots.1.name", "manual"),
				),
			},
		},
	})
}

// TestAccDtcloudVolume_shrinkRejected pins the rule that costs data if it is got
// wrong: the alternative to a plan-time error is a ForceNew that destroys it.
func TestAccDtcloudVolume_shrinkRejected(t *testing.T) {
	api := newFakeVolumeAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: volumeConfig(server.URL, "tf-acc-vol-shrink", "standard", "", 40),
			},
			{
				Config:      volumeConfig(server.URL, "tf-acc-vol-shrink", "standard", "", 10),
				ExpectError: regexp.MustCompile(`size cannot be reduced from 40 to 10`),
			},
		},
	})
}

// TestAccDtcloudVolume_shrinkRuleDoesNotBlockDestroy pins the other half of the
// grow-only rule: reject a shrink, and stay out of the way of a destroy. A disk
// grown outside Terraform makes every plan look like a shrink, teardown too.
func TestAccDtcloudVolume_shrinkRuleDoesNotBlockDestroy(t *testing.T) {
	api := newFakeVolumeAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	const name = "tf-acc-vol-oob-grow"
	config := acctest.ProviderConfig(server.URL) + fmt.Sprintf(`
resource "dtcloud_volume" "oob" {
  name           = %q
  size           = 20
  storage_policy = "standard"
}
`, name)

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr("dtcloud_volume.oob", "size", "20"),
			},
			{
				// Grown outside Terraform, so the next plan is a shrink and has to be refused.
				PreConfig:   func() { api.growOutOfBand(name, 40) },
				Config:      config,
				ExpectError: regexp.MustCompile(`size cannot be reduced from 40 to 20`),
			},
			{
				// And now the point: the same mismatch must not stop a destroy. An explicit
				// step, because the framework's own teardown does not refresh.
				Destroy: true,
				Config:  config,
			},
		},
	})
}

// TestAccDtcloudVolume_validation covers the rules worth catching during plan
// rather than part-way through an apply.
func TestAccDtcloudVolume_validation(t *testing.T) {
	api := newFakeVolumeAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				// The character rule on the name.
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_volume" "bad" {
  name           = "tf-acc-<script>"
  size           = 10
  storage_policy = "standard"
}
`,
				ExpectError: regexp.MustCompile(`must not contain any of`),
			},
			{
				// The name rule applies to the description too.
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_volume" "bad" {
  name           = "tf-acc-vol"
  size           = 10
  storage_policy = "standard"
  description    = "it's mine"
}
`,
				ExpectError: regexp.MustCompile(`must not contain any of`),
			},
			{
				// The minimum size.
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_volume" "bad" {
  name           = "tf-acc-vol"
  size           = 0
  storage_policy = "standard"
}
`,
				ExpectError: regexp.MustCompile(`expected size to be in the range \(1 - 8192\)`),
			},
			{
				// The maximum size.
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_volume" "bad" {
  name           = "tf-acc-vol"
  size           = 9000
  storage_policy = "standard"
}
`,
				ExpectError: regexp.MustCompile(`expected size to be in the range \(1 - 8192\)`),
			},
			{
				// The two create-time sources are mutually exclusive.
				Config: acctest.ProviderConfig(server.URL) + `
resource "dtcloud_volume" "bad" {
  name             = "tf-acc-vol"
  size             = 10
  storage_policy   = "standard"
  image_id         = "img-1"
  source_volume_id = "vol-1"
}
`,
				ExpectError: regexp.MustCompile(`conflicts with`),
			},
		},
	})
}

// TestAccDtcloudVolume_fromSnapshot covers the third create path: the restore
// goes to the snapshot service and answers with the raw volume keyed `id`. The
// counters prove where the request went.
func TestAccDtcloudVolume_fromSnapshot(t *testing.T) {
	api := newFakeVolumeAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	config := acctest.ProviderConfig(server.URL) + `
resource "dtcloud_volume" "restored" {
  name               = "tf-acc-restored"
  size               = 20
  storage_policy     = "standard"
  source_snapshot_id = "snap-0001"
}
`

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_volume.restored", "name", "tf-acc-restored"),
					resource.TestCheckResourceAttr("dtcloud_volume.restored", "size", "20"),
					resource.TestCheckResourceAttr("dtcloud_volume.restored", "status", "available"),
					// The id came out of a body keyed `id`, not `volumeId`.
					resource.TestCheckResourceAttrSet("dtcloud_volume.restored", "id"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.restores != 1 {
							return fmt.Errorf("expected one snapshot-to-volume call, saw %d", api.restores)
						}
						if api.creates != 0 || api.clones != 0 {
							return fmt.Errorf("the restore went to the wrong endpoint: %d creates, %d clones", api.creates, api.clones)
						}
						return nil
					},
				),
			},
			{
				// Nothing on a restored volume points back at the snapshot, so the re-plan
				// stays empty only because the resource never tries to read it back.
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

// TestAccDtcloudVolume_sourcesAreMutuallyExclusive pins the ConflictsWith wiring
// on all three create-time sources.
func TestAccDtcloudVolume_sourcesAreMutuallyExclusive(t *testing.T) {
	api := newFakeVolumeAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	pairs := []struct {
		name string
		hcl  string
	}{
		{"image and snapshot", `image_id = "img-1"
  source_snapshot_id = "snap-0001"`},
		{"clone and snapshot", `source_volume_id = "vol-9999"
  source_snapshot_id = "snap-0001"`},
		{"image and clone", `image_id = "img-1"
  source_volume_id = "vol-9999"`},
	}

	for _, pair := range pairs {
		t.Run(pair.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProviderFactories: acctest.ProviderFactories(),
				Steps: []resource.TestStep{
					{
						Config: acctest.ProviderConfig(server.URL) + fmt.Sprintf(`
resource "dtcloud_volume" "test" {
  name           = "tf-acc-conflict"
  size           = 20
  storage_policy = "standard"
  %s
}
`, pair.hcl),
						ExpectError: regexp.MustCompile(`(?s)conflicts with`),
					},
				},
			})
		})
	}
}
