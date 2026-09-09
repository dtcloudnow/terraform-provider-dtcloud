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

// checkTerraformOwnedGone asserts that everything this provider created has
// been destroyed. Volumes seeded directly into the fake are skipped by name —
// they stand in for infrastructure Terraform does not own.
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
//
// The update step is the one that matters. `osExtend` and `osRetype` are
// accepted while the volume is `available` and it stays `available` afterwards,
// so the fake holds the old size and policy for several reads. A waiter that
// only watched the status would return early and the apply would then fail with
// an inconsistent result — which is exactly what this test would report.
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
					// false from the details route, while the list route below
					// says true for this very same volume. The two handlers
					// compute it differently; see the note in the fake's list().
					resource.TestCheckResourceAttr("dtcloud_volume.test", "is_detachable", "false"),
					resource.TestCheckResourceAttr("dtcloud_volume.test", "created", acctest.FakeCreatedAt),
					// A blank volume carries no image metadata at all.
					resource.TestCheckResourceAttr("dtcloud_volume.test", "image_metadata.#", "0"),
					// The id came back as `volumeId`, not `id`.
					resource.TestCheckResourceAttrSet("dtcloud_volume.test", "id"),

					resource.TestCheckResourceAttr("data.dtcloud_volume.test", "name", "tf-acc-vol"),
					resource.TestCheckResourceAttr("data.dtcloud_volume.test", "size", "20"),
					resource.TestCheckResourceAttr("data.dtcloud_volume.test", "bootable", "false"),

					// The list endpoint reports "20 GB"; the provider parses it
					// so that `size` means the same thing everywhere.
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
						// The description is applied as a follow-up update,
						// because create accepts no description field.
						if api.updates != 1 {
							return fmt.Errorf("expected one update call to set the description, saw %d", api.updates)
						}
						return nil
					},
				),
			},
			{
				// Re-applying the same config must be a no-op — in particular
				// `description`, which the API never reports back, must not
				// look like a change on every plan.
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
				// Grow, and nothing else.
				//
				// This step is deliberately on its own. Changing the size and
				// the policy together made the test useless: the retype wait
				// polled long enough for the size to settle behind it, so
				// reverting the size wait to a status-only one still passed.
				// With only the size changing there is nothing to hide behind —
				// a status-only wait returns while the volume still reports
				// 20 GB, and the apply fails on an inconsistent result.
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
				// Retype, and nothing else — the same trap on the other field.
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
				// The details endpoint builds its response field by field and
				// leaves the description out, so there is nothing to import it
				// from. Documented on the resource page rather than papered over.
				ImportStateVerifyIgnore: []string{"description"},
			},
		},
	})
}

// TestAccDtcloudVolume_fromImage covers the bootable path: a different create
// status to wait through, a string "true" to turn into a bool, and the image
// metadata block.
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
					// Reported as a string by OpenStack; kept as one.
					resource.TestCheckResourceAttr("dtcloud_volume.boot", "image_metadata.0.min_ram", "512"),
					resource.TestCheckResourceAttr("dtcloud_volume.boot", "status", "available"),
				),
			},
			{
				// `image_id` is never echoed back, so the only thing keeping it
				// stable is that Read leaves it alone. If Read ever started
				// setting it from image_metadata, this plan would propose a
				// replacement.
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

// TestAccDtcloudVolume_clone pins the second create shape. The clone route
// answers with the raw OpenStack volume, keyed `id`, where create answers with
// a built response keyed `volumeId`.
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

// The VM a volume gets attached to in TestAccDtcloudVolume_attached. It never
// exists as an object — only as the identity the details endpoint reports on an
// attached volume, which is all this package ever sees of it.
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
// applies to a volume someone has attached to a VM.
//
// None of it was reachable before. Every volume in every other test is
// detached, so `attached_to`, `attached_to_id`, `attached_to_status`, the true
// branch of `is_detachable` and the `attached_to_id` filter were all dead, and
// so was the most consequential one: volumeSettled treating `in-use` as a
// resting state. Deleting `in-use` from that function used to leave the whole
// suite green.
//
// That last one is not academic. Growing a disk that is in use is the ordinary
// production case, and an attached volume stays `in-use` throughout the extend
// rather than passing through `available` — so a waiter that does not accept
// `in-use` hangs until timeout instead of returning.
//
// No VM is created. This package has no code that attaches anything; it only
// reads the state the details endpoint reports, so the fake is put into that
// state directly. See attachTo() for what that does and does not prove.
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
					// The filter has nothing to match yet, which is the half of
					// it that proves it filters at all.
					resource.TestCheckResourceAttr("data.dtcloud_volumes.on_vm", "volumes.#", "0"),
				),
			},
			{
				// Attached out of band — by dtcloud_vm_volume_attachment, or by
				// hand. Either way this package only ever sees the result.
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

					// Now it matches. The list route reports isDetachable as
					// true for a detached volume and the details route as
					// false, so the two disagree above and agree here.
					resource.TestCheckResourceAttr("data.dtcloud_volumes.on_vm", "volumes.#", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_volumes.on_vm", "volumes.0.name", name),
					resource.TestCheckResourceAttr("data.dtcloud_volumes.on_vm", "volumes.0.status", "in-use"),
					resource.TestCheckResourceAttr("data.dtcloud_volumes.on_vm", "volumes.0.attached_to", attachedVMName),
				),
			},
			{
				// The step this test exists for: grow a volume that is in use.
				// The status never leaves `in-use`, so the wait ends only if
				// volumeSettled accepts it — and only once the size lands.
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
				// Destroying while still attached must be refused, and refused
				// with something the reader can act on. The platform will not
				// delete an in-use volume under any circumstances — the web
				// console enforces the same rule — so the provider checks first
				// rather than letting the platform answer with its generic
				// five-condition complaint half-way through a destroy.
				//
				// It must not detach on its own either. Pulling a disk out of a
				// running machine to make a destroy succeed is the same class of
				// silent damage as shrinking a volume to satisfy a plan.
				Destroy:     true,
				Config:      attachedVolumeConfig(server.URL, name, "standard", 40),
				ExpectError: regexp.MustCompile(`still attached to .*detach it first`),
			},
			{
				// Detached, which is the supported way round. Also required
				// before the framework's own teardown can succeed.
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
// not own, which is the case the data source exists for: seeing what a cascade
// delete would take with it.
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

// TestAccDtcloudVolume_shrinkRejected pins the rule that costs data if it is
// got wrong. Shrinking is impossible, and the alternative to a plan-time error
// would be a ForceNew that silently destroys the volume to satisfy the plan.
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
// grow-only rule: it must reject a shrink and it must stay out of the way of a
// destroy.
//
// Someone grows a disk outside Terraform; the configuration still
// says the old size. From then on the refresh reports the larger size, every
// plan looks like a shrink, and CustomizeDiff rejects it — including the plan
// `terraform destroy` builds. The resource becomes impossible to destroy
// without first editing the configuration to match a size the user never chose.
//
// The step below grows the volume behind Terraform's back and confirms a normal
// plan still refuses. What proves the fix is what happens afterwards: the
// framework's own teardown runs against that same mismatch, and a destroy plan
// carries no configuration for the shrink rule to have an opinion about.
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
				// Grown outside Terraform. The configuration still says 20, so
				// the next plan is a shrink and has to be refused.
				PreConfig:   func() { api.growOutOfBand(name, 40) },
				Config:      config,
				ExpectError: regexp.MustCompile(`size cannot be reduced from 40 to 20`),
			},
			{
				// And now the point: the same mismatch must not stop a destroy.
				// An explicit destroy step rather than the framework's own
				// teardown, because the teardown does not refresh — it would
				// plan from a state that still said 20 and never reach the
				// disagreement this test is about.
				Destroy: true,
				Config:  config,
			},
		},
	})
}

// TestAccDtcloudVolume_validation covers the API rules worth catching
// during plan rather than as a 406 part-way through an apply.
func TestAccDtcloudVolume_validation(t *testing.T) {
	api := newFakeVolumeAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				// strictNoHtmlRegex on the name.
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
				// The API's minimum size.
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
				// The API's maximum size.
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

// TestAccDtcloudVolume_fromSnapshot covers the third create path.
//
// `source_snapshot_id` does not go to the volume create endpoint, which accepts
// no snapshot id. It goes to the snapshot service, and the response is the raw
// volume object keyed `id` — the clone shape from a third endpoint, with
// nothing typed for createdVolumeID to prefer.
//
// The restore itself is exercised rather than asserted about from the outside:
// if the provider sent the request to POST /volumes instead, `creates` would
// move and `restores` would not.
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
				// A restored volume is an ordinary volume; nothing on it points
				// back at the snapshot. So `source_snapshot_id` cannot be read
				// back and the re-plan has to stay empty on the strength of the
				// resource never trying to.
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

// TestAccDtcloudVolume_sourcesAreMutuallyExclusive pins the ConflictsWith wiring
// on all three create-time sources. Each pair is a different endpoint, and a
// configuration naming two of them has no defensible meaning.
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
