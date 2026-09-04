package snapshot_test

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
// been destroyed. Snapshots seeded into the fake are skipped by name: they
// stand in for infrastructure Terraform does not own.
func checkTerraformOwnedGone(api *fakeSnapshotAPI) func(*terraform.State) error {
	return func(*terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		for id, s := range api.snapshots {
			if !strings.HasPrefix(s.Name, "tf-acc-") {
				continue
			}
			if !s.deleted {
				return fmt.Errorf("snapshot %s (%s) still exists after destroy", id, s.Name)
			}
		}
		return nil
	}
}

// snapshotConfig is the whole surface of the package in one configuration: the
// resource, both data sources, and the plural one filtered by volume.
func snapshotConfig(endpoint, name, volumeID, description string) string {
	desc := ""
	if description != "" {
		desc = fmt.Sprintf("  description = %q\n", description)
	}
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(`
resource "dtcloud_snapshot" "test" {
  name      = %q
  volume_id = %q
%s}

data "dtcloud_snapshot" "test" {
  id = dtcloud_snapshot.test.id
}

data "dtcloud_snapshots" "all" {
  depends_on = [dtcloud_snapshot.test]
}

data "dtcloud_snapshots" "by_volume" {
  volume_id  = %q
  depends_on = [dtcloud_snapshot.test]
}
`, name, volumeID, desc, volumeID)
}

// bareSnapshotConfig is the resource on its own. The data sources are left out
// where a step removes the snapshot underneath Terraform, since dtcloud_snapshot
// errors on a missing id and would fail the step for the wrong reason.
func bareSnapshotConfig(endpoint, name, volumeID, description string) string {
	desc := ""
	if description != "" {
		desc = fmt.Sprintf("  description = %q\n", description)
	}
	return acctest.ProviderConfig(endpoint) + fmt.Sprintf(`
resource "dtcloud_snapshot" "test" {
  name      = %q
  volume_id = %q
%s}
`, name, volumeID, desc)
}

// TestAccDtcloudSnapshot_lifecycle drives create → read → re-plan → in-place
// rename → import → destroy, with both data sources reading along.
//
// Create is a two-step operation: the endpoint accepts no description, so one
// arrives by a follow-up update. The fake fails loudly if a description is ever
// sent to create, so taking the shortcut cannot pass quietly.
func TestAccDtcloudSnapshot_lifecycle(t *testing.T) {
	api := newFakeSnapshotAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: snapshotConfig(server.URL, "tf-acc-snap", "vol-0001", "nightly copy"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "name", "tf-acc-snap"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "volume_id", "vol-0001"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "description", "nightly copy"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "status", "available"),
					// Size comes from the source volume.
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "size", "20"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "project_id", "proj-0001"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "created_at", acctest.FakeCreatedAt),
					// The raw id, and the policy name resolved from it.
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "volume_type_id", "type-0001"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "storage_policy", "standard"),
					resource.TestCheckResourceAttrSet("dtcloud_snapshot.test", "id"),

					// The singular data source reads the same endpoint, so it
					// reports the description too.
					resource.TestCheckResourceAttr("data.dtcloud_snapshot.test", "name", "tf-acc-snap"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshot.test", "description", "nightly copy"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshot.test", "size", "20"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshot.test", "storage_policy", "standard"),

					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.#", "1"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.name", "tf-acc-snap"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.volume_id", "vol-0001"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.size", "20"),
					// The list reports the id; the provider resolves the name so
					// the two data sources cannot disagree.
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.volume_type_id", "type-0001"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.storage_policy", "standard"),
					// updated_at is null until something is written.
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.created_at", acctest.FakeCreatedAt),

					resource.TestCheckResourceAttr("data.dtcloud_snapshots.by_volume", "snapshots.#", "1"),

					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						// Exactly one follow-up PUT to carry the description
						// the create route cannot take.
						if api.updates != 1 {
							return fmt.Errorf("expected one update call to set the description, saw %d", api.updates)
						}
						return nil
					},
				),
			},
			{
				// Nothing changed, so nothing may be planned. Catches a Read
				// that invents a value.
				Config:   snapshotConfig(server.URL, "tf-acc-snap", "vol-0001", "nightly copy"),
				PlanOnly: true,
			},
			{
				// Rename alone. The fake holds the old name for several reads
				// while the status never leaves `available`, so a waiter
				// watching the status would return early.
				//
				// The description is untouched on purpose; the next step is the
				// mirror image, so neither half can cover for the other.
				Config: snapshotConfig(server.URL, "tf-acc-snap-renamed", "vol-0001", "nightly copy"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "name", "tf-acc-snap-renamed"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "description", "nightly copy"),
					// Written on the first update.
					resource.TestCheckResourceAttrSet("dtcloud_snapshot.test", "updated_at"),
				),
			},
			{
				// Re-describe alone, for the same reason in reverse.
				Config: snapshotConfig(server.URL, "tf-acc-snap-renamed", "vol-0001", "weekly copy"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "name", "tf-acc-snap-renamed"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "description", "weekly copy"),
				),
			},
			{
				// Everything round-trips, description included.
				ResourceName:      "dtcloud_snapshot.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccDtcloudSnapshot_createWaitsForAvailable pins the one thing every other
// test here lets slip: create must not return while the snapshot is still
// being written.
//
// It needs its own test because any configuration carrying a description has a
// second wait after create, and the status settles behind that wait — so a
// create wait that returned too early would be invisible. There is no
// description here and the status is asserted directly.
func TestAccDtcloudSnapshot_createWaitsForAvailable(t *testing.T) {
	api := newFakeSnapshotAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: bareSnapshotConfig(server.URL, "tf-acc-wait", "vol-0001", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "status", "available"),
					// The copy is finished, not merely started.
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "progress", "100%"),
				),
			},
		},
	})
}

// TestAccDtcloudSnapshot_volumeIDForcesNew pins that pointing a snapshot at
// another volume is a replacement, not an update: nothing re-points an existing
// snapshot, so an in-place plan would promise what the API cannot do.
func TestAccDtcloudSnapshot_volumeIDForcesNew(t *testing.T) {
	api := newFakeSnapshotAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: snapshotConfig(server.URL, "tf-acc-forcenew", "vol-0001", ""),
				Check: resource.TestCheckResourceAttr(
					"dtcloud_snapshot.test", "size", "20"),
			},
			{
				Config: snapshotConfig(server.URL, "tf-acc-forcenew", "vol-0002", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "volume_id", "vol-0002"),
					// The new snapshot inherits the new volume's size, which is
					// how a replacement is told apart from an update.
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "size", "40"),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						if api.creates != 2 {
							return fmt.Errorf("expected the volume change to force a second create, saw %d", api.creates)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccDtcloudSnapshot_descriptionCanBeCleared pins the clear-by-null path.
//
// The API removes a description only when sent an explicit JSON null; an empty
// string is rejected and an absent field leaves the value alone. The fake
// enforces all three, so a provider that sent the empty string, or nothing at
// all, fails here.
func TestAccDtcloudSnapshot_descriptionCanBeCleared(t *testing.T) {
	api := newFakeSnapshotAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: snapshotConfig(server.URL, "tf-acc-desc", "vol-0001", "written once"),
				Check: resource.TestCheckResourceAttr(
					"dtcloud_snapshot.test", "description", "written once"),
			},
			{
				// Changing it to something else.
				Config: snapshotConfig(server.URL, "tf-acc-desc", "vol-0001", "written twice"),
				Check: resource.TestCheckResourceAttr(
					"dtcloud_snapshot.test", "description", "written twice"),
			},
			{
				// Removing it from the configuration removes it on the platform.
				Config: snapshotConfig(server.URL, "tf-acc-desc", "vol-0001", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "description", ""),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						for _, s := range api.snapshots {
							if s.Name == "tf-acc-desc" && s.Description != "" {
								return fmt.Errorf("the description was not cleared on the platform: %q", s.Description)
							}
						}
						return nil
					},
				),
			},
			{
				// And it stays cleared, rather than being proposed again.
				Config:   snapshotConfig(server.URL, "tf-acc-desc", "vol-0001", ""),
				PlanOnly: true,
			},
			{
				// The other spelling of the same thing: `description = ""` and
				// no description argument both mean "no description", so
				// neither may error and neither may diff against the other.
				Config: acctest.ProviderConfig(server.URL) + fmt.Sprintf(`
resource "dtcloud_snapshot" "test" {
  name        = %q
  volume_id   = %q
  description = ""
}
`, "tf-acc-desc", "vol-0001"),
				PlanOnly: true,
			},
		},
	})
}

// TestAccDtcloudSnapshot_outOfBandDescriptionIsReconciled covers a description
// added outside Terraform on a snapshot whose configuration has none: it is
// removed, because the configuration is the truth, and the snapshot then
// destroys without complaint.
//
// The explicit Destroy step matters — the framework's own teardown does not
// refresh, so state and reality would never disagree during it.
func TestAccDtcloudSnapshot_outOfBandDescriptionIsReconciled(t *testing.T) {
	api := newFakeSnapshotAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	// The configuration never mentions a description. The platform grows one
	// underneath it, standing in for somebody using the web console.
	noDescription := bareSnapshotConfig(server.URL, "tf-acc-destroy", "vol-0001", "")

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: noDescription,
				// The mutation below is drift the next plan has to notice.
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "description", ""),
					func(*terraform.State) error {
						api.mu.Lock()
						defer api.mu.Unlock()
						for _, s := range api.snapshots {
							if s.Name == "tf-acc-destroy" {
								s.Description = "added in the web console"
							}
						}
						return nil
					},
				),
			},
			{
				// Refresh sees it, the configuration has none, so it goes.
				Config: noDescription,
				Check: func(*terraform.State) error {
					api.mu.Lock()
					defer api.mu.Unlock()
					for _, s := range api.snapshots {
						if s.Name == "tf-acc-destroy" && s.Description != "" {
							return fmt.Errorf("the out-of-band description survived: %q", s.Description)
						}
					}
					return nil
				},
			},
			{
				Config:  noDescription,
				Destroy: true,
			},
		},
	})
}

// TestAccDtcloudSnapshot_storagePolicyLookupIsBestEffort pins that resolving
// the policy name is a convenience, not a dependency: when it cannot be read
// the name is left empty and everything else is still reported.
func TestAccDtcloudSnapshot_storagePolicyLookupIsBestEffort(t *testing.T) {
	api := newFakeSnapshotAPI()
	api.policiesFail = true
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: snapshotConfig(server.URL, "tf-acc-nopolicy", "vol-0001", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					// The raw id is still reported; only the name is missing.
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "volume_type_id", "type-0001"),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "storage_policy", ""),
					resource.TestCheckResourceAttr("dtcloud_snapshot.test", "status", "available"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.volume_type_id", "type-0001"),
					resource.TestCheckResourceAttr("data.dtcloud_snapshots.all", "snapshots.0.storage_policy", ""),
				),
			},
		},
	})
}

// TestAccDtcloudSnapshot_deletedOutsideTerraform pins the not-found path: a
// snapshot removed elsewhere is dropped from state so the next apply takes a
// new one. The fake answers with the API's nested error shape, so this
// exercises the structured branch of dterr.IsNotFound rather than its fallback.
func TestAccDtcloudSnapshot_deletedOutsideTerraform(t *testing.T) {
	api := newFakeSnapshotAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	config := bareSnapshotConfig(server.URL, "tf-acc-vanish", "vol-0001", "")

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		CheckDestroy:      checkTerraformOwnedGone(api),
		Steps: []resource.TestStep{
			{
				Config: config,
				// The plan that follows is meant to propose creating it again.
				ExpectNonEmptyPlan: true,
				Check: func(*terraform.State) error {
					api.mu.Lock()
					defer api.mu.Unlock()
					for _, s := range api.snapshots {
						if s.Name == "tf-acc-vanish" {
							s.deleted = true
						}
					}
					return nil
				},
			},
			{
				// The refresh fails to find it, the resource leaves state, and
				// the plan proposes creating it again.
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccDtcloudSnapshot_rejectsHTMLInName keeps the character rule a plan-time
// error rather than a rejection halfway through an apply.
func TestAccDtcloudSnapshot_rejectsHTMLInName(t *testing.T) {
	api := newFakeSnapshotAPI()
	server := httptest.NewServer(api)
	defer server.Close()

	resource.UnitTest(t, resource.TestCase{
		ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config:      snapshotConfig(server.URL, "tf-acc-<script>", "vol-0001", ""),
				ExpectError: regexp.MustCompile(`must not contain any of`),
			},
		},
	})
}
